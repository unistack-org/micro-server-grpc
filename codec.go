package grpc

import (
	"io"

	"github.com/unistack-org/micro/v3/codec"
	"google.golang.org/grpc/encoding"
)

type wrapMicroCodec struct{ codec.Codec }

func (w *wrapMicroCodec) Name() string {
	return w.Codec.String()
}

type wrapGrpcCodec struct{ encoding.Codec }

func (w *wrapGrpcCodec) String() string {
	return w.Codec.Name()
}

func (w *wrapGrpcCodec) Marshal(v interface{}) ([]byte, error) {
	if m, ok := v.(*codec.Frame); ok {
		return m.Data, nil
	}
	return w.Codec.Marshal(v)
}

func (w *wrapGrpcCodec) Unmarshal(d []byte, v interface{}) error {
	if d == nil || v == nil {
		return nil
	}
	if m, ok := v.(*codec.Frame); ok {
		m.Data = d
		return nil
	}
	return w.Codec.Unmarshal(d, v)
}

func (w *wrapGrpcCodec) ReadHeader(conn io.Reader, m *codec.Message, mt codec.MessageType) error {
	return nil
}

func (w *wrapGrpcCodec) ReadBody(conn io.Reader, v interface{}) error {
	if m, ok := v.(*codec.Frame); ok {
		_, err := conn.Read(m.Data)
		return err
	}
	return codec.ErrInvalidMessage
}

func (w *wrapGrpcCodec) Write(conn io.Writer, m *codec.Message, v interface{}) error {
	// if we don't have a body
	if v != nil {
		b, err := w.Marshal(v)
		if err != nil {
			return err
		}
		m.Body = b
	}
	// write the body using the framing codec
	_, err := conn.Write(m.Body)
	return err
}
