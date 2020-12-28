package grpc

import (
	"io"

	"github.com/unistack-org/micro/v3/codec"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

type wrapStream struct{ grpc.ServerStream }

func (w *wrapStream) Write(d []byte) (int, error) {
	n := len(d)
	err := w.ServerStream.SendMsg(&codec.Frame{Data: d})
	return n, err
}

func (w *wrapStream) Read(d []byte) (int, error) {
	m := &codec.Frame{}
	err := w.ServerStream.RecvMsg(m)
	d = m.Data
	return len(d), err
}

type wrapMicroCodec struct{ codec.Codec }

func (w *wrapMicroCodec) Name() string {
	return w.Codec.String()
}

type wrapGrpcCodec struct{ encoding.Codec }

func (w *wrapGrpcCodec) String() string {
	return w.Codec.Name()
}

func (w *wrapGrpcCodec) Marshal(v interface{}) ([]byte, error) {
	switch m := v.(type) {
	case *codec.Frame:
		return m.Data, nil
	}
	return w.Codec.Marshal(v)
}

func (w wrapGrpcCodec) Unmarshal(d []byte, v interface{}) error {
	if d == nil || v == nil {
		return nil
	}
	switch m := v.(type) {
	case *codec.Frame:
		m.Data = d
		return nil
	}
	return w.Codec.Unmarshal(d, v)
}

func (g *wrapGrpcCodec) ReadHeader(conn io.Reader, m *codec.Message, mt codec.MessageType) error {
	return nil
}

func (g *wrapGrpcCodec) ReadBody(conn io.Reader, v interface{}) error {
	// caller has requested a frame
	switch m := v.(type) {
	case *codec.Frame:
		_, err := conn.Read(m.Data)
		return err
	}
	return codec.ErrInvalidMessage
}

func (g *wrapGrpcCodec) Write(conn io.Writer, m *codec.Message, v interface{}) error {
	// if we don't have a body
	if v != nil {
		b, err := g.Marshal(v)
		if err != nil {
			return err
		}
		m.Body = b
	}
	// write the body using the framing codec
	_, err := conn.Write(m.Body)
	return err
}
