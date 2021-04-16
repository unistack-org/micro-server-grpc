module github.com/unistack-org/micro-server-grpc/v3

go 1.15

require (
	github.com/golang/protobuf v1.5.2
	github.com/unistack-org/micro/v3 v3.3.13
	golang.org/x/net v0.0.0-20210415231046-e915ea6b2b7d
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/grpc v1.37.0
	google.golang.org/protobuf v1.26.0
)

//replace github.com/unistack-org/micro/v3 => ../../micro
