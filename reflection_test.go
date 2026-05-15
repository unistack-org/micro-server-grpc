package grpc

import (
	"fmt"
	"testing"
	
	"go.unistack.org/micro/v5/server"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

type reflector struct{}

func (r *reflector) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	fd, err := protoregistry.GlobalFiles.FindFileByPath(path)
	if err != nil {
		fmt.Printf("err: %v\n", err)
		return nil, err
	}
	return fd, nil
}

func (r *reflector) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	fd, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	if err != nil {
		return nil, err
	}
	return fd, nil
}

func (r *reflector) GetServiceInfo() map[string]grpc.ServiceInfo {
	fmt.Printf("GetServiceInfo\n")
	return nil
}

func (r *reflector) FindExtensionByName(field protoreflect.FullName) (protoreflect.ExtensionType, error) {
	fmt.Printf("FindExtensionByName field %#+v\n", field)
	return nil, nil
}

func (r *reflector) FindExtensionByNumber(message protoreflect.FullName, field protoreflect.FieldNumber) (protoreflect.ExtensionType, error) {
	fmt.Printf("FindExtensionByNumber message %#+v field %#+v\n", message, field)
	return nil, nil
}

func (r *reflector) RangeExtensionsByMessage(message protoreflect.FullName, f func(protoreflect.ExtensionType) bool) {
	fmt.Printf("RangeExtensionsByMessage\n")
}

func TestReflector(t *testing.T) {
	t.Skip()
	srv := NewServer(Reflection(&reflector{}), server.Address(":12345"))
	if err := srv.Init(); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Logf("addr %s", srv.Options().Address)
	select {}
}
