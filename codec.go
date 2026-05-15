package grpc

import (
	"go.unistack.org/micro/v5/codec"
	"google.golang.org/grpc/encoding"
)

var (
	_ codec.Codec    = &wrapGrpcCodec{}
	_ encoding.Codec = &wrapMicroCodec{}
)

type wrapMicroCodec struct{ codec.Codec }

func (w *wrapMicroCodec) Name() string {
	return w.Codec.String()
}

func (w *wrapMicroCodec) Marshal(v interface{}) ([]byte, error) {
	return w.Codec.Marshal(v)
}

func (w *wrapMicroCodec) Unmarshal(d []byte, v interface{}) error {
	return w.Codec.Unmarshal(d, v)
}

type wrapGrpcCodec struct{ encoding.Codec }

func (w *wrapGrpcCodec) String() string {
	return w.Codec.Name()
}

func (w *wrapGrpcCodec) Marshal(v interface{}, opts ...codec.Option) ([]byte, error) {
	if m, ok := v.(*codec.Frame); ok {
		return m.Data, nil
	}
	return w.Codec.Marshal(v)
}

func (w *wrapGrpcCodec) Unmarshal(d []byte, v interface{}, opts ...codec.Option) error {
	if d == nil || v == nil {
		return nil
	}
	if m, ok := v.(*codec.Frame); ok {
		m.Data = d
		return nil
	}
	return w.Codec.Unmarshal(d, v)
}
