package grpc

import (
	"go.unistack.org/micro/v5/codec"
	"go.unistack.org/micro/v5/metadata"
	"go.unistack.org/micro/v5/server"
)

var _ server.Response = &rpcResponse{}

type rpcResponse struct {
	header metadata.Metadata
	codec  codec.Codec
}

func (r *rpcResponse) Codec() codec.Codec {
	return r.codec
}

func (r *rpcResponse) WriteHeader(hdr metadata.Metadata) {
	for k, v := range hdr {
		r.header[k] = v
	}
}

func (r *rpcResponse) Write(b []byte) error {
	return nil
}
