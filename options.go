package grpc

import (
	"context"

	"github.com/unistack-org/micro/v3/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

type (
	codecsKey     struct{}
	grpcOptions   struct{}
	maxMsgSizeKey struct{}
	reflectionKey struct{}
)

// gRPC Codec to be used to encode/decode requests for a given content type
func Codec(contentType string, c encoding.Codec) server.Option {
	return func(o *server.Options) {
		codecs := make(map[string]encoding.Codec)
		if o.Context == nil {
			o.Context = context.Background()
		}
		if v, ok := o.Context.Value(codecsKey{}).(map[string]encoding.Codec); ok && v != nil {
			codecs = v
		}
		codecs[contentType] = c
		o.Context = context.WithValue(o.Context, codecsKey{}, codecs)
	}
}

// Options to be used to configure gRPC options
func Options(opts ...grpc.ServerOption) server.Option {
	return setServerOption(grpcOptions{}, opts)
}

//
// MaxMsgSize set the maximum message in bytes the server can receive and
// send.  Default maximum message size is 4 MB.
//
func MaxMsgSize(s int) server.Option {
	return setServerOption(maxMsgSizeKey{}, s)
}

// Reflection enables reflection support in grpc server
func Reflection(b bool) server.Option {
	return setServerOption(reflectionKey{}, b)
}
