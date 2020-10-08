package grpc_test

import (
	"testing"

	pb "github.com/unistack-org/micro-server-grpc/internal/errors"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestErrors(t *testing.T) {
	any, err := anypb.New(&pb.Error{})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("srv any: %#+v\n", any)
}
