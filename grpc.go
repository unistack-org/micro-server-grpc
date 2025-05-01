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

func (w *streamWrapper) Context() context.Context {
	if w.ctx != nil {
		return w.ctx
	}
	return w.ServerStream.Context()
}

type Server struct {
	handlers       map[string]server.Handler
	srv            *grpc.Server
	exit           chan chan error
	rsvc           *register.Service
	rpc            *rServer
	opts           server.Options
	unknownHandler grpc.StreamHandler
	sync.RWMutex
	stateLive   *atomic.Uint32
	stateReady  *atomic.Uint32
	stateHealth *atomic.Uint32
	started     bool
	registered  bool
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
		exit:        make(chan chan error),
		stateLive:   &atomic.Uint32{},
		stateReady:  &atomic.Uint32{},
		stateHealth: &atomic.Uint32{},
	}

	g.opts.Meter = g.opts.Meter.Clone(meter.Labels("type", "grpc"))

	return g
}

func (g *Server) configure(opts ...server.Option) error {
	g.Lock()
	defer g.Unlock()

	for _, o := range opts {
		o(&g.opts)
	}

	if g.opts.Context != nil {
		if codecs, ok := g.opts.Context.Value(codecsKey{}).(map[string]encoding.Codec); ok && codecs != nil {
			for k, v := range codecs {
				g.opts.Codecs[k] = &wrapGrpcCodec{v}
			}
		}
	}

	for _, k := range g.opts.Codecs {
		encoding.RegisterCodec(&wrapMicroCodec{k})
	}

	maxMsgSize := g.getMaxMsgSize()

	gopts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.UnknownServiceHandler(g.handler),
	}

	if opts := g.getGrpcOptions(); opts != nil {
		gopts = append(opts, gopts...)
	}

	g.rsvc = nil
	restart := false
	if g.started {
		restart = true
		if err := g.Stop(); err != nil {
			return err
		}
	}
	g.srv = grpc.NewServer(gopts...)

	if v, ok := g.opts.Context.Value(reflectionKey{}).(Reflector); ok {
		reflectionv1pb.RegisterServerReflectionServer(
			g.srv,
			greflection.NewServerV1(greflection.ServerOptions{
				Services:           v,
				DescriptorResolver: v,
				ExtensionResolver:  v,
			}),
		)
	}

	if h, ok := g.opts.Context.Value(unknownServiceHandlerKey{}).(grpc.StreamHandler); ok {
		g.unknownHandler = h
	}

	if restart {
		return g.Start()
	}

	return nil
}

func (g *Server) getMaxMsgSize() int {
	s, ok := g.opts.Context.Value(maxMsgSizeKey{}).(int)
	if !ok {
		return 4 * 1024 * 1024
	}
	return s
}

func (g *Server) getGrpcOptions() []grpc.ServerOption {
	if g.opts.Context == nil {
		return nil
	}

	opts, ok := g.opts.Context.Value(grpcOptions{}).([]grpc.ServerOption)
	if !ok || opts == nil {
		return nil
	}

	return opts
}

func (g *Server) handler(srv interface{}, stream grpc.ServerStream) error {
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
		ctx, sp = g.opts.Tracer.Start(ctx, "rpc-server",
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
		g.opts.Meter.Counter(semconv.ServerRequestInflight, "endpoint", endpointName, "server", "grpc").Inc()
		defer func() {
			te := time.Since(ts)
			g.opts.Meter.Summary(semconv.ServerRequestLatencyMicroseconds, "endpoint", endpointName, "server", "grpc").Update(te.Seconds())
			g.opts.Meter.Histogram(semconv.ServerRequestDurationSeconds, "endpoint", endpointName, "server", "grpc").Update(te.Seconds())
			g.opts.Meter.Counter(semconv.ServerRequestInflight, "endpoint", endpointName, "server", "grpc").Dec()

			st := status.Convert(err)
			if st == nil || st.Code() == codes.OK {
				g.opts.Meter.Counter(semconv.ServerRequestTotal, "endpoint", endpointName, "server", "grpc", "status", "success", "code", strconv.Itoa(int(codes.OK))).Inc()
			} else {
				g.opts.Meter.Counter(semconv.ServerRequestTotal, "endpoint", endpointName, "server", "grpc", "status", "failure", "code", strconv.Itoa(int(st.Code()))).Inc()
			}
		}()
	}

	if g.opts.Wait != nil {
		g.opts.Wait.Add(1)
		defer g.opts.Wait.Done()
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

	g.rpc.mu.RLock()
	svc := g.rpc.serviceMap[serviceName]
	g.rpc.mu.RUnlock()

	if svc == nil {
		if g.unknownHandler != nil {
			err = g.unknownHandler(srv, stream)
			return err
		}
		err = status.New(codes.Unimplemented, fmt.Sprintf("unknown service %s", serviceName)).Err()
		return err
	}

	mtype := svc.method[methodName]
	if mtype == nil {
		if g.unknownHandler != nil {
			err = g.unknownHandler(srv, stream)
			return err
		}
		err = status.New(codes.Unimplemented, fmt.Sprintf("unknown service method %s.%s", serviceName, methodName)).Err()
		return err
	}

	// process unary
	if !mtype.stream {
		err = g.processRequest(ctx, stream, svc, mtype, ct)
	} else {
		// process stream
		err = g.processStream(ctx, stream, svc, mtype, ct)
	}

	return err
}

func (g *Server) processRequest(ctx context.Context, stream grpc.ServerStream, service *service, mtype *methodType, ct string) error {
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
		service:     g.opts.Name,
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

	g.opts.Hooks.EachPrev(func(hook options.Hook) {
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
		var errStatus *status.Status
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
		case (interface{ GRPCStatus() *status.Status }):
			errStatus = verr.GRPCStatus()
		default:
			g.RLock()
			config := g.opts
			g.RUnlock()
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

func (g *Server) processStream(ctx context.Context, stream grpc.ServerStream, service *service, mtype *methodType, ct string) error {
	opts := g.opts

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
			if g.opts.Logger.V(logger.ErrorLevel) {
				g.opts.Logger.Error(g.opts.Context, "handler error will not be transferred properly, must return *errors.Error or proto.Message")
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

func (g *Server) Options() server.Options {
	g.RLock()
	opts := g.opts
	g.RUnlock()

	return opts
}

func (g *Server) Init(opts ...server.Option) error {
	return g.configure(opts...)
}

func (g *Server) NewHandler(h interface{}, opts ...server.HandlerOption) server.Handler {
	return newRPCHandler(h, opts...)
}

func (g *Server) Handle(h server.Handler) error {
	if err := g.rpc.register(h.Handler()); err != nil {
		return err
	}

	g.handlers[h.Name()] = h
	return nil
}

func (g *Server) Register() error {
	g.RLock()
	rsvc := g.rsvc
	config := g.opts
	g.RUnlock()

	// if service already filled, reuse it and return early
	if rsvc != nil {
		if err := server.DefaultRegisterFunc(rsvc, config); err != nil {
			return err
		}
		return nil
	}

	service, err := server.NewRegisterService(g)
	if err != nil {
		return err
	}

	g.RLock()
	// Maps are ordered randomly, sort the keys for consistency
	handlerList := make([]string, 0, len(g.handlers))
	for n := range g.handlers {
		// Only advertise non internal handlers
		handlerList = append(handlerList, n)
	}

	sort.Strings(handlerList)

	g.RUnlock()

	g.RLock()
	registered := g.registered
	g.RUnlock()

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

	g.Lock()
	defer g.Unlock()

	g.registered = true
	g.rsvc = service

	return nil
}

func (g *Server) Deregister() error {
	var err error

	g.RLock()
	config := g.opts
	g.RUnlock()

	service, err := server.NewRegisterService(g)
	if err != nil {
		return err
	}

	if config.Logger.V(logger.InfoLevel) {
		config.Logger.Info(config.Context, "Deregistering node: "+service.Nodes[0].ID)
	}

	if err := server.DefaultDeregisterFunc(service, config); err != nil {
		return err
	}

	g.Lock()
	g.rsvc = nil

	if !g.registered {
		g.Unlock()
		return nil
	}

	g.registered = false

	g.Unlock()
	return nil
}

func (g *Server) Start() error {
	g.RLock()
	if g.started {
		g.RUnlock()
		return nil
	}
	g.RUnlock()

	config := g.Options()

	// micro: config.Transport.Listen(config.Address)
	var ts net.Listener
	var err error

	if l := config.Listener; l != nil {
		ts = l
	} else {
		// check the tls config for secure connect
		if tc := config.TLSConfig; tc != nil {
			ts, err = tls.Listen("tcp", config.Address, tc)
			// otherwise just plain tcp listener
		} else {
			ts, err = net.Listen("tcp", config.Address)
		}
		if err != nil {
			return err
		}
	}

	if config.MaxConn > 0 {
		ts = netutil.LimitListener(ts, config.MaxConn)
	}

	if config.Logger.V(logger.InfoLevel) {
		config.Logger.Info(config.Context, "Server [grpc] Listening on "+ts.Addr().String())
	}
	g.Lock()
	g.opts.Address = ts.Addr().String()
	if len(g.opts.Advertise) == 0 {
		g.opts.Advertise = ts.Addr().String()
	}
	g.Unlock()

	// use RegisterCheck func before register
	// nolint: nestif
	if err = g.opts.RegisterCheck(config.Context); err != nil {
		if config.Logger.V(logger.ErrorLevel) {
			config.Logger.Error(config.Context, fmt.Sprintf("Server %s-%s register check error", config.Name, config.ID), err)
		}
	} else {
		// announce self to the world
		if err = g.Register(); err != nil {
			if config.Logger.V(logger.ErrorLevel) {
				config.Logger.Error(config.Context, "Server register error", err)
			}
		}
	}

	// micro: go ts.Accept(s.accept)
	go func() {
		if err = g.srv.Serve(ts); err != nil {
			if config.Logger.V(logger.ErrorLevel) {
				config.Logger.Error(config.Context, "gRPC Server start error", err)
			}
			if err = g.Stop(); err != nil {
				if config.Logger.V(logger.ErrorLevel) {
					config.Logger.Error(config.Context, "gRPC Server stop error", err)
				}
			}
		}
		g.stateLive.Store(1)
		g.stateReady.Store(1)
		g.stateHealth.Store(1)
	}()

	go func() {
		t := new(time.Ticker)

		// only process if it exists
		if g.opts.RegisterInterval > time.Duration(0) {
			// new ticker
			t = time.NewTicker(g.opts.RegisterInterval)
		}

		// return error chan
		var ch chan error

	Loop:
		for {
			select {
			// register self on interval
			case <-t.C:
				g.RLock()
				registered := g.registered
				g.RUnlock()
				rerr := g.opts.RegisterCheck(g.opts.Context)
				// nolint: nestif
				if rerr != nil && registered {
					if config.Logger.V(logger.ErrorLevel) {
						config.Logger.Error(config.Context, fmt.Sprintf("Server %s-%s register check error, deregister it", config.Name, config.ID), rerr)
					}
					// deregister self in case of error
					if err = g.Deregister(); err != nil {
						if config.Logger.V(logger.ErrorLevel) {
							config.Logger.Error(config.Context, fmt.Sprintf("Server %s-%s deregister error", config.Name, config.ID), err)
						}
					}
				} else if rerr != nil && !registered {
					if config.Logger.V(logger.ErrorLevel) {
						config.Logger.Error(config.Context, fmt.Sprintf("Server %s-%s register check error", config.Name, config.ID), rerr)
					}
					continue
				}
				if err = g.Register(); err != nil {
					if config.Logger.V(logger.ErrorLevel) {
						config.Logger.Error(config.Context, fmt.Sprintf("Server %s-%s register error", config.Name, config.ID), err)
					}
				}
			// wait for exit
			case ch = <-g.exit:
				break Loop
			}
		}

		// deregister self
		if err = g.Deregister(); err != nil {
			if config.Logger.V(logger.ErrorLevel) {
				config.Logger.Error(config.Context, "Server deregister error", err)
			}
		}

		// wait for waitgroup
		if g.opts.Wait != nil {
			g.opts.Wait.Wait()
		}

		// stop the grpc server
		exit := make(chan bool)

		go func() {
			g.srv.GracefulStop()
			close(exit)
			g.stateLive.Store(0)
			g.stateReady.Store(0)
			g.stateHealth.Store(0)
		}()

		select {
		case <-exit:
		case <-time.After(g.opts.GracefulTimeout):
			g.srv.Stop()
			g.stateLive.Store(0)
			g.stateReady.Store(0)
			g.stateHealth.Store(0)
		}

		// close transport
		ch <- nil

		if config.Logger.V(logger.InfoLevel) {
			config.Logger.Info(config.Context, fmt.Sprintf("broker [%s] Disconnected from %s", config.Broker.String(), config.Broker.Address()))
		}
		// disconnect broker
		if err = config.Broker.Disconnect(config.Context); err != nil {
			if config.Logger.V(logger.ErrorLevel) {
				config.Logger.Error(config.Context, fmt.Sprintf("broker [%s] disconnect error", config.Broker.String()), err)
			}
		}
	}()

	// mark the server as started
	g.Lock()
	g.started = true
	g.Unlock()

	return nil
}

func (g *Server) Stop() error {
	g.RLock()
	if !g.started {
		g.RUnlock()
		return nil
	}
	g.RUnlock()

	ch := make(chan error)
	g.exit <- ch

	err := <-ch
	g.Lock()
	g.rsvc = nil
	g.started = false
	g.Unlock()

	return err
}

func (g *Server) String() string {
	return "grpc"
}

func (g *Server) Name() string {
	return g.opts.Name
}

func (g *Server) GRPCServer() *grpc.Server {
	return g.srv
}

func (g *Server) Live() bool {
	return g.stateLive.Load() == 1
}

func (g *Server) Ready() bool {
	return g.stateReady.Load() == 1
}

func (g *Server) Health() bool {
	return g.stateHealth.Load() == 1
}

func NewServer(opts ...server.Option) *Server {
	return newServer(opts...)
}
