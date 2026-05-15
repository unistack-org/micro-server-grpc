package grpc

import (
	"reflect"

	"go.unistack.org/micro/v5/server"
)

type rpcHandler struct {
	opts    server.HandlerOptions
	handler interface{}
	name    string
}

func newRPCHandler(handler interface{}, opts ...server.HandlerOption) server.Handler {
	options := server.NewHandlerOptions(opts...)

	hdlr := reflect.ValueOf(handler)
	name := reflect.Indirect(hdlr).Type().Name()

	return &rpcHandler{
		name:    name,
		handler: handler,
		opts:    options,
	}
}

func (r *rpcHandler) Name() string {
	return r.name
}

func (r *rpcHandler) Handler() interface{} {
	return r.handler
}

func (r *rpcHandler) Options() server.HandlerOptions {
	return r.opts
}
