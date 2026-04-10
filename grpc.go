// Package grpc provides a grpc server
package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	greflection "google.golang.org/grpc/reflection"
	reflectionv1pb "google.golang.org/grpc/reflection/grpc_reflection_v1"

	// nolint: staticcheck

	"go.unistack.org/micro/v4/errors"
	"go.unistack.org/micro/v4/logger"
	"go.unistack.org/micro/v4/metadata"
	"go.unistack.org/micro/v4/meter"
	"go.unistack.org/micro/v4/options"
	"go.unistack.org/micro/v4/register"
	"go.unistack.org/micro/v4/semconv"
	"go.unistack.org/micro/v4/server"
	"go.unistack.org/micro/v4/tracer"
	"golang.org/x/net/netutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	gmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
)

const (
	DefaultContentType = "application/grpc"
)

type streamWrapper struct {
	ctx context.Context
	grpc.ServerStream
}

func (sw *streamWrapper) Context() context.Context {
	if sw.ctx != nil {
		return sw.ctx
	}
	return sw.ServerStream.Context()
}

type Server struct {
	handlers         map[string]server.Handler
	srv              *grpc.Server
	listener         net.Listener
	exit             chan struct{}
	rsvc             *register.Service
	rpc              *rServer
	opts             server.Options
	unknownHandler   grpc.StreamHandler
	mu               sync.RWMutex
	shutdownStopOnce sync.Once
	stateLive        *atomic.Uint32
	stateReady       *atomic.Uint32
	stateHealth      *atomic.Uint32
	started          atomic.Bool
	registered       bool
	// reflection  bool
}

func newServer(opts ...server.Option) *Server {
	// create a grpc server
	g := &Server{
		opts: server.NewOptions(opts...),
		rpc: &rServer{
			serviceMap: make(map[string]*service),
		},
		handlers:    make(map[string]server.Handler),
		exit:        make(chan struct{}),
		stateLive:   &atomic.Uint32{},
		stateReady:  &atomic.Uint32{},
		stateHealth: &atomic.Uint32{},
	}

	g.opts.Meter = g.opts.Meter.Clone(meter.Labels("type", "grpc"))

	return g
}

func (s *Server) configure(opts ...server.Option) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, o := range opts {
		o(&s.opts)
	}

	if s.opts.Context != nil {
		if codecs, ok := s.opts.Context.Value(codecsKey{}).(map[string]encoding.Codec); ok && codecs != nil {
			for k, v := range codecs {
				s.opts.Codecs[k] = &wrapGrpcCodec{v}
			}
		}
	}

	for _, k := range s.opts.Codecs {
		encoding.RegisterCodec(&wrapMicroCodec{k})
	}

	maxMsgSize := s.getMaxMsgSize()

	gopts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.UnknownServiceHandler(s.handler),
	}

	if opts := s.getGrpcOptions(); opts != nil {
		gopts = append(opts, gopts...)
	}

	s.rsvc = nil
	restart := false
	if s.started.Load() {
		restart = true
		if err := s.Stop(); err != nil {
			return err
		}
	}
	s.srv = grpc.NewServer(gopts...)

	if v, ok := s.opts.Context.Value(reflectionKey{}).(Reflector); ok {
		reflectionv1pb.RegisterServerReflectionServer(
			s.srv,
			greflection.NewServerV1(greflection.ServerOptions{
				Services:           v,
				DescriptorResolver: v,
				ExtensionResolver:  v,
			}),
		)
	}

	if h, ok := s.opts.Context.Value(unknownServiceHandlerKey{}).(grpc.StreamHandler); ok {
		s.unknownHandler = h
	}

	if restart {
		return s.Start()
	}

	return nil
}

func (s *Server) getMaxMsgSize() int {
	size, ok := s.opts.Context.Value(maxMsgSizeKey{}).(int)
	if !ok {
		return 4 * 1024 * 1024
	}
	return size
}

func (s *Server) getGrpcOptions() []grpc.ServerOption {
	if s.opts.Context == nil {
		return nil
	}

	opts, ok := s.opts.Context.Value(grpcOptions{}).([]grpc.ServerOption)
	if !ok || opts == nil {
		return nil
	}

	return opts
}

func (s *Server) handler(srv interface{}, stream grpc.ServerStream) error {
	var err error

	ctx := stream.Context()

	fullMethod, ok := grpc.MethodFromServerStream(stream)
	if !ok {
		return status.Errorf(codes.Internal, "method does not exist in context")
	}

	var gmd map[string][]string
	// get grpc metadata
	gmd, ok = gmetadata.FromIncomingContext(ctx)
	if !ok {
		gmd = gmetadata.MD{}
	}

	var serviceName, methodName string
	serviceName, methodName, err = serviceMethod(fullMethod)
	if err != nil {
		err = status.New(codes.InvalidArgument, err.Error()).Err()
		return err
	}

	endpointName := serviceName + "/" + methodName

	ts := time.Now()
	var sp tracer.Span
	if !slices.Contains(tracer.DefaultSkipEndpoints, endpointName) {
		ctx, sp = s.opts.Tracer.Start(ctx, "rpc-server",
			tracer.WithSpanKind(tracer.SpanKindServer),
			tracer.WithSpanLabels(
				"endpoint", endpointName,
				"server", "grpc",
			),
		)
		defer func() {
			st := status.Convert(err)
			if st != nil || st.Code() != codes.OK {
				sp.SetStatus(tracer.SpanStatusError, err.Error())
			}
			sp.Finish()
		}()
	}

	md := metadata.Copy(gmd)

	md.Set("path", fullMethod)
	md.Set("micro-server", "grpc")
	md.Set(metadata.HeaderEndpoint, methodName)
	md.Set(metadata.HeaderService, serviceName)

	var td string
	// timeout for server deadline
	if v := md.Get("timeout"); v != nil {
		md.Del("timeout")
		td = v[0]
	}
	if v := md.Get("grpc-timeout"); v != nil {
		md.Del("grpc-timeout")
		td = v[0][:len(v)-1]
		switch v[0][len(v)-1:] {
		case "S":
			td += "s"
		case "M":
			td += "m"
		case "H":
			td += "h"
		case "m":
			td += "ms"
		case "u":
			td += "us"
		case "n":
			td += "ns"
		}
	}

	// get content type
	ct := DefaultContentType

	if ctype := md.Get("content-type"); ctype != nil {
		ct = ctype[0]
	}

	// create new context
	ctx = metadata.NewIncomingContext(ctx, md)
	ctx = metadata.NewOutgoingContext(ctx, metadata.New(0))
	ctx = context.WithValue(ctx, rspMetadataKey{}, &rspMetadataVal{m: metadata.New(0)})

	stream = &streamWrapper{ctx, stream}

	if !slices.Contains(meter.DefaultSkipEndpoints, endpointName) {
		s.opts.Meter.Counter(semconv.ServerRequestInflight, "endpoint", endpointName, "server", "grpc").Inc()
		defer func() {
			te := time.Since(ts)
			s.opts.Meter.Summary(semconv.ServerRequestLatencyMicroseconds, "endpoint", endpointName, "server", "grpc").Update(te.Seconds())
			s.opts.Meter.Histogram(semconv.ServerRequestDurationSeconds, "endpoint", endpointName, "server", "grpc").Update(te.Seconds())
			s.opts.Meter.Counter(semconv.ServerRequestInflight, "endpoint", endpointName, "server", "grpc").Dec()

			st := status.Convert(err)
			if st == nil || st.Code() == codes.OK {
				s.opts.Meter.Counter(semconv.ServerRequestTotal, "endpoint", endpointName, "server", "grpc", "status", "success", "code", strconv.Itoa(int(codes.OK))).Inc()
			} else {
				s.opts.Meter.Counter(semconv.ServerRequestTotal, "endpoint", endpointName, "server", "grpc", "status", "failure", "code", strconv.Itoa(int(st.Code()))).Inc()
			}
		}()
	}

	if s.opts.Wait != nil {
		s.opts.Wait.Add(1)
		defer s.opts.Wait.Done()
	}

	// get peer from context
	if p, ok := peer.FromContext(ctx); ok {
		md.Set("remote", p.Addr.String())
		ctx = peer.NewContext(ctx, p)
	}

	// set the timeout if we have it
	if len(td) > 0 {
		var n uint64
		if n, err = strconv.ParseUint(td, 10, 64); err == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(n))
			defer cancel()
		}
	}

	s.rpc.mu.RLock()
	svc := s.rpc.serviceMap[serviceName]
	s.rpc.mu.RUnlock()

	if svc == nil {
		if s.unknownHandler != nil {
			err = s.unknownHandler(srv, stream)
			return err
		}
		err = status.New(codes.Unimplemented, fmt.Sprintf("unknown service %s", serviceName)).Err()
		return err
	}

	mtype := svc.method[methodName]
	if mtype == nil {
		if s.unknownHandler != nil {
			err = s.unknownHandler(srv, stream)
			return err
		}
		err = status.New(codes.Unimplemented, fmt.Sprintf("unknown service method %s.%s", serviceName, methodName)).Err()
		return err
	}

	// process unary
	if !mtype.stream {
		err = s.processRequest(ctx, stream, svc, mtype, ct)
	} else {
		// process stream
		err = s.processStream(ctx, stream, svc, mtype, ct)
	}

	return err
}

func (s *Server) processRequest(ctx context.Context, stream grpc.ServerStream, service *service, mtype *methodType, ct string) error {
	// for {
	var err error
	var argv, replyv reflect.Value

	// Decode the argument value.
	argIsValue := false // if true, need to indirect before calling.
	if mtype.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(mtype.ArgType.Elem())
	} else {
		argv = reflect.New(mtype.ArgType)
		argIsValue = true
	}

	// Unmarshal request
	if err = stream.RecvMsg(argv.Interface()); err != nil {
		return err
	}

	if argIsValue {
		argv = argv.Elem()
	}

	// reply value
	replyv = reflect.New(mtype.ReplyType.Elem())

	function := mtype.method.Func
	var returnValues []reflect.Value

	// create a client.Request
	r := &rpcRequest{
		service:     s.opts.Name,
		contentType: ct,
		method:      fmt.Sprintf("%s.%s", service.name, mtype.method.Name),
		endpoint:    fmt.Sprintf("%s.%s", service.name, mtype.method.Name),
		payload:     argv.Interface(),
	}
	// define the handler func
	fn := func(ctx context.Context, req server.Request, rsp interface{}) (err error) {
		returnValues = function.Call([]reflect.Value{service.rcvr, mtype.prepareContext(ctx), argv, reflect.ValueOf(rsp)})

		// The return value for the method is an error.
		if rerr := returnValues[0].Interface(); rerr != nil {
			err = rerr.(error)
		}

		return err
	}

	s.opts.Hooks.EachPrev(func(hook options.Hook) {
		if h, ok := hook.(server.HookHandler); ok {
			fn = h(fn)
		}
	})

	statusCode := codes.OK
	statusDesc := ""
	// execute the handler
	appErr := fn(ctx, r, replyv.Interface())
	if md := getResponseMetadata(ctx); len(md) > 0 {
		if err = stream.SendHeader(md.AsHTTP2()); err != nil {
			return err
		}
	}
	if appErr != nil {
		var err error
		var errStatus *status.Status
		var ok bool
		errStatus, ok = status.FromError(appErr)
		if ok {
			return errStatus.Err()
		}
		if errStatus = status.FromContextError(appErr); errStatus.Code() != codes.Unknown {
			return errStatus.Err()
		}
		switch verr := appErr.(type) {
		case *errors.Error:
			statusCode = microError(verr)
			statusDesc = verr.Error()
			errStatus = status.New(statusCode, statusDesc)
		case proto.Message:
			// user defined error that proto based we can attach it to grpc status
			statusCode = convertCode(appErr)
			statusDesc = appErr.Error()
			errStatus, err = status.New(statusCode, statusDesc).WithDetails(protoadapt.MessageV1Of(verr))
			if err != nil {
				return err
			}
		default:
			s.mu.RLock()
			config := s.opts
			s.mu.RUnlock()
			if config.Logger.V(logger.ErrorLevel) {
				config.Logger.Error(config.Context, "handler error will not be transferred properly, must return *errors.Error or proto.Message")
			}
			// default case user pass own error type that not proto based
			statusCode = convertCode(verr)
			statusDesc = verr.Error()
			errStatus = status.New(statusCode, statusDesc)
		}

		return errStatus.Err()
	}

	if err := stream.SendMsg(replyv.Interface()); err != nil {
		return err
	}

	return status.New(statusCode, statusDesc).Err()
}

func (s *Server) processStream(ctx context.Context, stream grpc.ServerStream, service *service, mtype *methodType, ct string) error {
	opts := s.opts

	r := &rpcRequest{
		service:     opts.Name,
		contentType: ct,
		method:      fmt.Sprintf("%s.%s", service.name, mtype.method.Name),
		endpoint:    fmt.Sprintf("%s.%s", service.name, mtype.method.Name),
		stream:      true,
	}

	ss := &rpcStream{
		ServerStream: stream,
		request:      r,
	}

	function := mtype.method.Func
	var returnValues []reflect.Value

	// Invoke the method, providing a new value for the reply.
	fn := func(ctx context.Context, req server.Request, stream interface{}) error {
		returnValues = function.Call([]reflect.Value{service.rcvr, mtype.prepareContext(ctx), reflect.ValueOf(stream)})
		if err := returnValues[0].Interface(); err != nil {
			return err.(error)
		}

		return nil
	}

	opts.Hooks.EachPrev(func(hook options.Hook) {
		if h, ok := hook.(server.HookHandler); ok {
			fn = h(fn)
		}
	})

	statusCode := codes.OK
	statusDesc := ""

	appErr := fn(ctx, r, ss)
	if md := getResponseMetadata(ctx); len(md) > 0 {
		if err := stream.SendHeader(md.AsHTTP2()); err != nil {
			return err
		}
	}
	if appErr != nil {
		var err error
		var errStatus *status.Status
		var ok bool
		errStatus, ok = status.FromError(appErr)
		if ok {
			return errStatus.Err()
		}
		if errStatus = status.FromContextError(appErr); errStatus.Code() != codes.Unknown {
			return errStatus.Err()
		}
		switch verr := appErr.(type) {
		case *errors.Error:
			statusCode = microError(verr)
			statusDesc = verr.Error()
			errStatus = status.New(statusCode, statusDesc)
		case proto.Message:
			// user defined error that proto based we can attach it to grpc status
			statusCode = convertCode(appErr)
			statusDesc = appErr.Error()
			errStatus, err = status.New(statusCode, statusDesc).WithDetails(protoadapt.MessageV1Of(verr))
			if err != nil {
				return err
			}
		default:
			if s.opts.Logger.V(logger.ErrorLevel) {
				s.opts.Logger.Error(s.opts.Context, "handler error will not be transferred properly, must return *errors.Error or proto.Message")
			}
			// default case user pass own error type that not proto based
			statusCode = convertCode(verr)
			statusDesc = verr.Error()
			errStatus = status.New(statusCode, statusDesc)
		}

		return errStatus.Err()
	}

	return status.New(statusCode, statusDesc).Err()
}

func (s *Server) Options() server.Options {
	s.mu.RLock()
	opts := s.opts
	s.mu.RUnlock()

	return opts
}

func (s *Server) Init(opts ...server.Option) error {
	return s.configure(opts...)
}

func (s *Server) NewHandler(h interface{}, opts ...server.HandlerOption) server.Handler {
	return newRPCHandler(h, opts...)
}

func (s *Server) Handle(h server.Handler) error {
	if err := s.rpc.register(h.Handler()); err != nil {
		return err
	}

	s.handlers[h.Name()] = h
	return nil
}

func (s *Server) Register() error {
	s.mu.RLock()
	rsvc := s.rsvc
	config := s.opts
	s.mu.RUnlock()

	// if service already filled, reuse it and return early
	if rsvc != nil {
		if err := server.DefaultRegisterFunc(rsvc, config); err != nil {
			return err
		}
		return nil
	}

	service, err := server.NewRegisterService(s)
	if err != nil {
		return err
	}

	s.mu.RLock()
	// Maps are ordered randomly, sort the keys for consistency
	handlerList := make([]string, 0, len(s.handlers))
	for n := range s.handlers {
		// Only advertise non internal handlers
		handlerList = append(handlerList, n)
	}

	sort.Strings(handlerList)

	s.mu.RUnlock()

	s.mu.RLock()
	registered := s.registered
	s.mu.RUnlock()

	if !registered {
		if config.Logger.V(logger.InfoLevel) {
			config.Logger.Info(config.Context, fmt.Sprintf("Register [%s] Registering node: %s", config.Register.String(), service.Nodes[0].ID))
		}
	}

	// register the service
	if err := server.DefaultRegisterFunc(service, config); err != nil {
		return err
	}

	// already registered? don't need to register subscribers
	if registered {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.registered = true
	s.rsvc = service

	return nil
}

func (s *Server) Deregister() error {
	var err error

	s.mu.RLock()
	config := s.opts
	s.mu.RUnlock()

	service, err := server.NewRegisterService(s)
	if err != nil {
		return err
	}

	if config.Logger.V(logger.InfoLevel) {
		config.Logger.Info(config.Context, "Deregistering node: "+service.Nodes[0].ID)
	}

	if err := server.DefaultDeregisterFunc(service, config); err != nil {
		return err
	}

	s.mu.Lock()
	s.rsvc = nil

	if !s.registered {
		s.mu.Unlock()
		return nil
	}

	s.registered = false

	s.mu.Unlock()
	return nil
}

func (s *Server) Start() (err error) {
	if !s.started.CompareAndSwap(false, true) {
		return nil
	}

	defer func() {
		if err != nil {
			s.started.Store(false)
		}
	}()

	cfg := s.Options()

	// micro: config.Transport.Listen(config.Address)
	var ts net.Listener

	if l := cfg.Listener; l != nil {
		ts = l
	} else {
		// check the tls config for secure connect
		if tc := cfg.TLSConfig; tc != nil {
			ts, err = tls.Listen("tcp", cfg.Address, tc)
			// otherwise just plain tcp listener
		} else {
			ts, err = net.Listen("tcp", cfg.Address)
		}
		if err != nil {
			return err
		}
	}

	if cfg.MaxConn > 0 {
		ts = netutil.LimitListener(ts, cfg.MaxConn)
	}

	if cfg.Logger.V(logger.InfoLevel) {
		cfg.Logger.Info(cfg.Context, "Server [grpc] Listening on "+ts.Addr().String())
	}
	s.mu.Lock()
	s.listener = ts
	s.opts.Address = ts.Addr().String()
	if len(s.opts.Advertise) == 0 {
		s.opts.Advertise = ts.Addr().String()
	}
	s.mu.Unlock()

	// use RegisterCheck func before register
	// nolint: nestif
	if err = s.opts.RegisterCheck(cfg.Context); err != nil {
		if cfg.Logger.V(logger.ErrorLevel) {
			cfg.Logger.Error(cfg.Context, fmt.Sprintf("Server %s-%s register check error", cfg.Name, cfg.ID), err)
		}
	} else {
		// announce self to the world
		if err = s.Register(); err != nil {
			if cfg.Logger.V(logger.ErrorLevel) {
				cfg.Logger.Error(cfg.Context, "Server register error", err)
			}
		}
	}

	// micro: go ts.Accept(s.accept)
	go func() {
		if err = s.srv.Serve(ts); err != nil {
			if cfg.Logger.V(logger.ErrorLevel) {
				cfg.Logger.Error(cfg.Context, "gRPC Server start error", err)
			}
			if err = s.Stop(); err != nil {
				if cfg.Logger.V(logger.ErrorLevel) {
					cfg.Logger.Error(cfg.Context, "gRPC Server stop error", err)
				}
			}
		}
		s.stateLive.Store(1)
		s.stateReady.Store(1)
		s.stateHealth.Store(1)
	}()

	go func() {
		t := new(time.Ticker)

		// only process if it exists
		if s.opts.RegisterInterval > time.Duration(0) {
			// new ticker
			t = time.NewTicker(s.opts.RegisterInterval)
		}

		for {
			select {
			// register self on interval
			case <-t.C:
				s.mu.RLock()
				registered := s.registered
				s.mu.RUnlock()
				rerr := s.opts.RegisterCheck(s.opts.Context)
				// nolint: nestif
				if rerr != nil && registered {
					if cfg.Logger.V(logger.ErrorLevel) {
						cfg.Logger.Error(cfg.Context, fmt.Sprintf("Server %s-%s register check error, deregister it", cfg.Name, cfg.ID), rerr)
					}
					// deregister self in case of error
					if err = s.Deregister(); err != nil {
						if cfg.Logger.V(logger.ErrorLevel) {
							cfg.Logger.Error(cfg.Context, fmt.Sprintf("Server %s-%s deregister error", cfg.Name, cfg.ID), err)
						}
					}
				} else if rerr != nil && !registered {
					if cfg.Logger.V(logger.ErrorLevel) {
						cfg.Logger.Error(cfg.Context, fmt.Sprintf("Server %s-%s register check error", cfg.Name, cfg.ID), rerr)
					}
					continue
				}
				if err = s.Register(); err != nil {
					if cfg.Logger.V(logger.ErrorLevel) {
						cfg.Logger.Error(cfg.Context, fmt.Sprintf("Server %s-%s register error", cfg.Name, cfg.ID), err)
					}
				}
			// wait for exit
			case <-s.exit:
				t.Stop()
				return
			}
		}
	}()

	return nil
}

func (s *Server) Stop() error {
	if !s.started.CompareAndSwap(true, false) {
		return nil
	}

	return s.stop()
}

func (s *Server) stop() error {
	cfg := s.Options()
	if cfg.Logger.V(logger.InfoLevel) {
		cfg.Logger.Info(cfg.Context, "Graceful shutdown initiated")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.GracefulTimeout)
	defer cancel()

	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			if cfg.Logger.V(logger.InfoLevel) {
				cfg.Logger.Info(ctx, "Listener close error", err)
			}
		}
	}

	close(s.exit)

	// deregister self
	if err := s.Deregister(); err != nil {
		if cfg.Logger.V(logger.ErrorLevel) {
			cfg.Logger.Error(cfg.Context, "Server deregister error", err)
		}
	}

	// wait for waitgroup
	if s.opts.Wait != nil {
		waitDone := make(chan struct{})
		go func() {
			s.opts.Wait.Wait()
			close(waitDone)
		}()

		select {
		case <-waitDone:
			if cfg.Logger.V(logger.InfoLevel) {
				cfg.Logger.Info(ctx, "All active goroutines(requests) completed")
			}
		case <-ctx.Done():
			if cfg.Logger.V(logger.WarnLevel) {
				cfg.Logger.Warn(ctx, "Graceful timeout exceeded")
			}
		}
	}

	grpcErr := error(nil)
	grpcStop := make(chan struct{})

	go func() {
		s.srv.GracefulStop()
		close(grpcStop)
	}()

	select {
	case <-grpcStop:
		if cfg.Logger.V(logger.InfoLevel) {
			cfg.Logger.Info(ctx, "gRPC server stopped graceful")
		}
	case <-ctx.Done():
		if cfg.Logger.V(logger.WarnLevel) {
			cfg.Logger.Warn(ctx, "gRPC server graceful timeout exceeded, forcing stop")
		}
		grpcErr = fmt.Errorf("gRPC shutdown timeout")
		s.srv.Stop()
	}

	// disconnect broker
	if cfg.Logger.V(logger.InfoLevel) {
		cfg.Logger.Info(ctx, fmt.Sprintf("broker [%s] Disconnected from %s", cfg.Broker.String(), cfg.Broker.Address()))
	}
	if err := cfg.Broker.Disconnect(ctx); err != nil {
		if cfg.Logger.V(logger.ErrorLevel) {
			cfg.Logger.Error(ctx, fmt.Sprintf("broker [%s] disconnect error", cfg.Broker.String()), err)
		}
	}

	s.stateLive.Store(0)
	s.stateReady.Store(0)
	s.stateHealth.Store(0)
	s.mu.Lock()
	s.rsvc = nil
	s.mu.Unlock()

	return grpcErr
}

func (s *Server) String() string {
	return "grpc"
}

func (s *Server) Name() string {
	return s.opts.Name
}

func (s *Server) GRPCServer() *grpc.Server {
	return s.srv
}

func (s *Server) Live() bool {
	return s.stateLive.Load() == 1
}

func (s *Server) Ready() bool {
	return s.stateReady.Load() == 1
}

func (s *Server) Health() bool {
	return s.stateHealth.Load() == 1
}

func NewServer(opts ...server.Option) *Server {
	return newServer(opts...)
}
