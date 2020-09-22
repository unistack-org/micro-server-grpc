package grpc

//go:generate protoc -I./errors -I. --go-grpc_out=paths=source_relative:./errors --go_out=paths=source_relative:./errors --micro_out=paths=source_relative:./errors errors/errors.proto
