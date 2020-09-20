package grpc

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/unistack-org/micro/v3/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

type codecsKey struct{}
type grpcOptions struct{}
type netListener struct{}
type maxMsgSizeKey struct{}
type maxConnKey struct{}
type tlsAuth struct{}
type reflectionKey struct{}

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

// AuthTLS should be used to setup a secure authentication using TLS
func AuthTLS(t *tls.Config) server.Option {
	return setServerOption(tlsAuth{}, t)
}

// MaxConn specifies maximum number of max simultaneous connections to server
func MaxConn(n int) server.Option {
	return setServerOption(maxConnKey{}, n)
}

// Listener specifies the net.Listener to use instead of the default
func Listener(l net.Listener) server.Option {
	return setServerOption(netListener{}, l)
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
