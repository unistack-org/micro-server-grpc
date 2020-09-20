package grpc_test

import (
	"context"
	"testing"

	bmemory "github.com/unistack-org/micro-broker-memory"
	gclient "github.com/unistack-org/micro-client-grpc"
	rmemory "github.com/unistack-org/micro-registry-memory"
	regRouter "github.com/unistack-org/micro-router-registry"
	gserver "github.com/unistack-org/micro-server-grpc"
	pberr "github.com/unistack-org/micro-server-grpc/errors"
	pb "github.com/unistack-org/micro-server-grpc/proto"
	"github.com/unistack-org/micro/v3/broker"
	"github.com/unistack-org/micro/v3/client"
	"github.com/unistack-org/micro/v3/router"
	"github.com/unistack-org/micro/v3/server"
)

type testServer struct {
	pb.UnimplementedTestServer
}

/*
func (g *testServer) Call(ctx context.Context, req *pb.Request, rsp *pb.Response) error {
	if req.Name == "Error" {
		st := status.New(codes.InvalidArgument, "error request")
		st, err := st.WithDetails(&pberr.Error{Id: "id", Code: 99, Detail: "detail"})
		if err != nil {
			return err
		}
		return st.Err()
	}
	rsp = &pb.Response{Msg: "Hello " + req.Name}
	return nil
}
*/

func (g *testServer) Call(ctx context.Context, req *pb.Request, rsp *pb.Response) error {
	if req.Name == "Error" {
		return &pberr.Error{Id: "id", Code: 99, Detail: "detail"}
	}
	rsp.Msg = "Hello " + req.Name
	return nil
}

func TestGRPCServer(t *testing.T) {
	var err error

	r := rmemory.NewRegistry()
	b := bmemory.NewBroker(broker.Registry(r))
	s := gserver.NewServer(server.Address(":12345"), server.Registry(r), server.Name("helloworld"), gserver.Reflection(true))
	// create router
	rtr := regRouter.NewRouter(router.Registry(r))

	h := &testServer{}
	err = pb.RegisterTestHandler(s, h)
	if err != nil {
		t.Fatalf("can't register handler: %v", err)
	}

	if err = s.Init(); err != nil {
		t.Fatal(err)
	}

	if err = s.Start(); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err = s.Stop(); err != nil {
			t.Fatal(err)
		}
	}()

	// create client
	c := gclient.NewClient(client.Router(rtr), client.Registry(r), client.Broker(b))

	testMethods := []string{
		"Test.Call",
	}

	for _, method := range testMethods {
		req := c.NewRequest("helloworld", method, &pb.Request{
			Name: "John",
		})

		rsp := pb.Response{}

		err = c.Call(context.TODO(), req, &rsp)
		if err != nil {
			t.Fatalf("method: %s err: %v", method, err)
		}

		if rsp.Msg != "Hello John" {
			t.Fatalf("Got unexpected response %v", rsp.Msg)
		}
	}

	//rsp := rpb.ServerReflectionResponse{}
	//req := c.NewRequest("helloworld", "Test.ServerReflectionInfo", &rpb.ServerReflectionRequest{}, client.StreamingRequest())
	//if err := c.Call(context.TODO(), req, &rsp); err != nil {
	//	t.Fatal(err)
	//}

	//	select {}
}
