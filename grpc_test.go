package grpc

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cgrpc "go.unistack.org/micro-client-grpc/v4"
	cp "go.unistack.org/micro-codec-proto/v4"
	testpb "go.unistack.org/micro-server-grpc/v4/proto"
	"go.unistack.org/micro/v4/broker"
	"go.unistack.org/micro/v4/client"
	"go.unistack.org/micro/v4/logger"
	"go.unistack.org/micro/v4/logger/slog"
	"go.unistack.org/micro/v4/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const addrGrpcSrc = ":9092"

var clientGrpc testpb.TestServiceClient

func TestMain(m *testing.M) {
	logger.DefaultLogger = slog.NewLogger(
		logger.WithLevel(logger.ParseLevel("debug")),
	)

	if err := logger.DefaultLogger.Init(); err != nil {
		panic(err)
	}

	clientGrpc = createClient(cgrpc.NewClient(
		client.Codec("application/grpc", cp.NewCodec()),
		client.Codec("application/proto", cp.NewCodec()),
	), addrGrpcSrc)

	os.Exit(m.Run())
}

type RequestStats struct {
	started   atomic.Int32
	completed atomic.Int32
	cancelled atomic.Int32
}

func (rs *RequestStats) Started() int32   { return rs.started.Load() }
func (rs *RequestStats) Completed() int32 { return rs.completed.Load() }
func (rs *RequestStats) Cancelled() int32 { return rs.cancelled.Load() }

type TestHandler struct {
	stats *RequestStats
}

func (h *TestHandler) Call(ctx context.Context, req *testpb.CallReq, rsp *testpb.CallRsp) error {
	h.stats.started.Add(1)

	duration, err := time.ParseDuration(req.Data)
	if err != nil {
		return err
	}

	select {
	case <-time.After(duration):
		h.stats.completed.Add(1)
		return nil

	case <-ctx.Done():
		h.stats.cancelled.Add(1)
		return status.Errorf(codes.Canceled, "request cancelled: %v", ctx.Err())
	}
}

func TestGracefulShutdown_AllRequestsComplete(t *testing.T) {
	const (
		requestCount    = 50
		requestDuration = "300ms"
		gracefulTimeout = 2 * time.Second
	)

	stats := &RequestStats{}
	h := &TestHandler{stats: stats}

	svc := startTestService(t, h, gracefulTimeout)

	var wg sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			_, err := clientGrpc.Call(ctx, &testpb.CallReq{Data: requestDuration})

			if err != nil {
				t.Logf("Request %d: %v", id, err)
			}
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(requestCount), stats.Started(), "all requests should start")

	t.Log("Triggering graceful shutdown")
	shutdownStart := time.Now()

	err := svc.Stop()
	shutdownDuration := time.Since(shutdownStart)

	wg.Wait()

	t.Logf("Stats: started=%d, completed=%d, cancelled=%d, shutdown_duration=%v",
		stats.Started(), stats.Completed(), stats.Cancelled(), shutdownDuration)

	assert.NoError(t, err, "graceful shutdown should succeed")
	assert.Equal(t, int32(requestCount), stats.Completed(), "all requests should complete")
	assert.Equal(t, int32(0), stats.Cancelled(), "no requests should be cancelled")

	assert.GreaterOrEqual(t, shutdownDuration, 300*time.Millisecond-50*time.Millisecond,
		"shutdown should wait for requests to complete")
	assert.Less(t, shutdownDuration, gracefulTimeout,
		"shutdown should complete within timeout")
}

func TestGracefulShutdown_TimeoutForceStop(t *testing.T) {
	const (
		requestCount    = 20
		shortRequestMs  = "100ms"
		longRequestMs   = "10000ms"
		gracefulTimeout = 500 * time.Millisecond
	)

	stats := &RequestStats{}
	h := &TestHandler{stats: stats}

	svc := startTestService(t, h, gracefulTimeout)

	var wg sync.WaitGroup
	for i := 0; i < requestCount/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			_, err := clientGrpc.Call(ctx, &testpb.CallReq{Data: shortRequestMs})

			if err != nil {
				t.Logf("Request %d: %v", id, err)
			}
		}(i)
	}
	for i := requestCount / 2; i < requestCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			_, err := clientGrpc.Call(ctx, &testpb.CallReq{Data: longRequestMs})

			if err != nil {
				t.Logf("Request %d: %v", id, err)
			}
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(requestCount), stats.Started(), "all requests should start")

	t.Log("Triggering graceful shutdown with timeout")
	shutdownStart := time.Now()
	err := svc.Stop()
	shutdownDuration := time.Since(shutdownStart)

	wg.Wait()

	t.Logf("Stats: started=%d, completed=%d, cancelled=%d, shutdown_duration=%v",
		stats.Started(), stats.Completed(), stats.Cancelled(), shutdownDuration)

	assert.Error(t, err, "shutdown should timeout")

	assert.Greater(t, stats.Completed(), int32(0), "some fast requests should complete")
	assert.Greater(t, stats.Cancelled(), int32(0), "some slow requests should be cancelled")
	assert.Equal(t, stats.Started(), stats.Completed()+stats.Cancelled(),
		"all requests accounted for")

	assert.Less(t, shutdownDuration, 2*time.Second,
		"shutdown should not wait for all long requests")
}

func TestGracefulShutdown_MixedDurations(t *testing.T) {
	const (
		gracefulTimeout = 1 * time.Second
	)

	stats := &RequestStats{}
	h := &TestHandler{stats: stats}

	svc := startTestService(t, h, gracefulTimeout)

	durationsFast := []string{"50ms", "100ms", "200ms", "500ms", "800ms"}
	durationSlow := []string{"5000ms", "8000ms", "10000ms"}

	var wg sync.WaitGroup
	for i, duration := range durationsFast {
		wg.Add(1)
		go func(id int, dur string) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			_, err := clientGrpc.Call(ctx, &testpb.CallReq{Data: dur})

			if err != nil {
				t.Logf("Request %d (%s): CANCELLED", id, dur)
			} else {
				t.Logf("Request %d (%s): COMPLETED", id, dur)
			}
		}(i, duration)
	}
	for i, duration := range durationSlow {
		wg.Add(1)
		go func(id int, dur string) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			_, err := clientGrpc.Call(ctx, &testpb.CallReq{Data: dur})

			if err != nil {
				t.Logf("Request %d (%s): CANCELLED", id, dur)
			} else {
				t.Logf("Request %d (%s): COMPLETED", id, dur)
			}
		}(i, duration)
	}

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(len(durationsFast)+len(durationSlow)), stats.Started(), "all requests should start")

	t.Log("Triggering graceful shutdown")
	shutdownStart := time.Now()
	err := svc.Stop()
	shutdownDuration := time.Since(shutdownStart)
	wg.Wait()

	t.Logf("Stats: started=%d, completed=%d, cancelled=%d, shutdown_duration=%v",
		stats.Started(), stats.Completed(), stats.Cancelled(), shutdownDuration)

	assert.GreaterOrEqual(t, stats.Completed(), int32(5),
		"fast requests (50, 100, 200, 500, 800ms) should complete")

	assert.Greater(t, stats.Cancelled(), int32(1),
		"requests >1s should be cancelled")

	assert.Error(t, err)

	assert.Less(t, shutdownDuration, 2*time.Second,
		"shutdown should timeout and not wait for all requests")
}

func TestGracefulShutdown_NoActiveRequests(t *testing.T) {
	const (
		gracefulTimeout = 100 * time.Millisecond
	)

	stats := &RequestStats{}
	h := &TestHandler{stats: stats}

	svc := startTestService(t, h, gracefulTimeout)

	t.Log("Triggering graceful shutdown...")
	shutdownStart := time.Now()

	err := svc.Stop()
	shutdownDuration := time.Since(shutdownStart)

	t.Logf("Stats: started=%d, completed=%d, cancelled=%d, shutdown_duration=%v",
		stats.Started(), stats.Completed(), stats.Cancelled(), shutdownDuration)

	assert.NoError(t, err, "graceful shutdown should succeed")
	assert.Less(t, shutdownDuration, 200*time.Millisecond,
		"shutdown with no requests should be instant")

	assert.Equal(t, int32(0), stats.Started())
	assert.Equal(t, int32(0), stats.Completed())
	assert.Equal(t, int32(0), stats.Cancelled())
}

func startTestService(t *testing.T, handler testpb.TestServiceServer, gracefulTimeout time.Duration) server.Server {
	var (
		defaultRegisterTTL      = 30 * time.Second
		defaultRegisterInterval = 2 * time.Second
	)

	srvOpts := []server.Option{
		server.Wait(nil),
		server.Name("test"),
		server.Version("version"),
		server.Address(addrGrpcSrc),
		server.RegisterTTL(defaultRegisterTTL),
		server.RegisterInterval(defaultRegisterInterval),
		server.Broker(broker.DefaultBroker),
		server.Codec("application/grpc", cp.NewCodec()),
		server.Codec("application/grpc+proto", cp.NewCodec()),
		server.GracefulTimeout(gracefulTimeout),
		server.Logger(logger.DefaultLogger),
	}

	srv := NewServer(srvOpts...)

	err := testpb.RegisterTestServiceServer(srv, handler)
	require.NoError(t, err)

	require.Nil(t, srv.Init())

	go func() {
		if err := srv.Start(); err != nil {
			t.Logf("Service run error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	return srv
}

func createClient(c client.Client, address string) testpb.TestServiceClient {
	return testpb.NewTestServiceClient(
		"testservice.grpc",
		client.NewClientCallOptions(c, client.WithAddress(address)),
	)
}
