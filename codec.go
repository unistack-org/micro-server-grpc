package grpc

import (
	b "bytes"
	"encoding/json"
	"strings"

	oldjsonpb "github.com/golang/protobuf/jsonpb"
	oldproto "github.com/golang/protobuf/proto"
	bytes "github.com/unistack-org/micro-codec-bytes"
	"github.com/unistack-org/micro/v3/codec"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/metadata"
	jsonpb "google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type jsonCodec struct{}
type bytesCodec struct{}
type protoCodec struct{}
type wrapCodec struct{ encoding.Codec }

var (
	jsonpbMarshaler = jsonpb.MarshalOptions{
		UseEnumNumbers:  false,
		EmitUnpopulated: false,
		UseProtoNames:   true,
		AllowPartial:    false,
	}

	jsonpbUnmarshaler = jsonpb.UnmarshalOptions{
		DiscardUnknown: false,
		AllowPartial:   false,
	}

	oldjsonpbMarshaler = oldjsonpb.Marshaler{
		OrigName:     true,
		EmitDefaults: false,
	}

	oldjsonpbUnmarshaler = oldjsonpb.Unmarshaler{
		AllowUnknownFields: false,
	}
)

var (
	defaultGRPCCodecs = map[string]encoding.Codec{
		"application/json":         jsonCodec{},
		"application/proto":        protoCodec{},
		"application/protobuf":     protoCodec{},
		"application/octet-stream": protoCodec{},
		"application/grpc":         protoCodec{},
		"application/grpc+json":    jsonCodec{},
		"application/grpc+proto":   protoCodec{},
		"application/grpc+bytes":   bytesCodec{},
	}
)

func (w wrapCodec) String() string {
	return w.Codec.Name()
}

func (w wrapCodec) Marshal(v interface{}) ([]byte, error) {
	switch m := v.(type) {
	case *bytes.Frame:
		return m.Data, nil
	}
	return w.Codec.Marshal(v)
}

func (w wrapCodec) Unmarshal(data []byte, v interface{}) error {
	if len(data) == 0 {
		return nil
	}
	if v == nil {
		return nil
	}
	switch m := v.(type) {
	case *bytes.Frame:
		m.Data = data
		return nil
	}
	return w.Codec.Unmarshal(data, v)
}

func (protoCodec) Marshal(v interface{}) ([]byte, error) {
	switch m := v.(type) {
	case proto.Message:
		return proto.Marshal(m)
	case oldproto.Message:
		return oldproto.Marshal(m)
	}
	return nil, codec.ErrInvalidMessage
}

func (protoCodec) Unmarshal(data []byte, v interface{}) error {
	if len(data) == 0 {
		return nil
	}
	if v == nil {
		return nil
	}
	switch m := v.(type) {
	case proto.Message:
		return proto.Unmarshal(data, m)
	case oldproto.Message:
		return oldproto.Unmarshal(data, m)
	}
	return codec.ErrInvalidMessage
}

func (protoCodec) Name() string {
	return "proto"
}

func (jsonCodec) Marshal(v interface{}) ([]byte, error) {
	switch m := v.(type) {
	case proto.Message:
		return jsonpbMarshaler.Marshal(m)
	case oldproto.Message:
		buf := b.NewBuffer(nil)
		err := oldjsonpbMarshaler.Marshal(buf, m)
		return buf.Bytes(), err
	}
	return json.Marshal(v)
}

func (jsonCodec) Unmarshal(data []byte, v interface{}) error {
	if len(data) == 0 {
		return nil
	}
	if v == nil {
		return nil
	}
	switch m := v.(type) {
	case proto.Message:
		return jsonpbUnmarshaler.Unmarshal(data, m)
	case oldproto.Message:
		return oldjsonpbUnmarshaler.Unmarshal(b.NewReader(data), m)
	}
	return json.Unmarshal(data, v)
}

func (jsonCodec) Name() string {
	return "json"
}

func (bytesCodec) Marshal(v interface{}) ([]byte, error) {
	switch m := v.(type) {
	case *[]byte:
		return *m, nil
	}
	return nil, codec.ErrInvalidMessage
}

func (bytesCodec) Unmarshal(data []byte, v interface{}) error {
	if len(data) == 0 {
		return nil
	}
	if v == nil {
		return nil
	}
	switch m := v.(type) {
	case *[]byte:
		*m = data
		return nil
	}
	return codec.ErrInvalidMessage
}

func (bytesCodec) Name() string {
	return "bytes"
}

type grpcCodec struct {
	grpc.ServerStream
	// headers
	id       string
	target   string
	method   string
	endpoint string

	c encoding.Codec
}

func (g *grpcCodec) ReadHeader(m *codec.Message, mt codec.MessageType) error {
	md, _ := metadata.FromIncomingContext(g.ServerStream.Context())
	if m == nil {
		m = new(codec.Message)
	}
	if m.Header == nil {
		m.Header = make(map[string]string, len(md))
	}
	for k, v := range md {
		m.Header[k] = strings.Join(v, ",")
	}
	m.Id = g.id
	m.Target = g.target
	m.Method = g.method
	m.Endpoint = g.endpoint
	return nil
}

func (g *grpcCodec) ReadBody(v interface{}) error {
	// caller has requested a frame
	switch m := v.(type) {
	case *bytes.Frame:
		return g.ServerStream.RecvMsg(m)
	}
	return g.ServerStream.RecvMsg(v)
}

func (g *grpcCodec) Write(m *codec.Message, v interface{}) error {
	// if we don't have a body
	if v != nil {
		b, err := g.c.Marshal(v)
		if err != nil {
			return err
		}
		m.Body = b
	}
	// write the body using the framing codec
	return g.ServerStream.SendMsg(&bytes.Frame{Data: m.Body})
}

func (g *grpcCodec) Close() error {
	return nil
}

func (g *grpcCodec) String() string {
	return "grpc"
}
