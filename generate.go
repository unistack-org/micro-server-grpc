package grpc

//go:generate go install go.unistack.org/protoc-gen-go-micro/v4@latest
//go:generate sh -c "protoc -I./proto -I$(go list -f '{{ .Dir }}' -m go.unistack.org/micro-proto/v4) -I. --go-micro_out=paths=source_relative,components="micro|rpc|client|server":./proto --go_out=paths=source_relative:./proto proto/test.proto"
