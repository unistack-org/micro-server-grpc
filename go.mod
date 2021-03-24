module github.com/unistack-org/micro-server-grpc/v3

go 1.15

require (
	github.com/golang/protobuf v1.5.1
	github.com/unistack-org/micro/v3 v3.3.1
	golang.org/x/net v0.0.0-20210324205630-d1beb07c2056
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/grpc v1.36.0
	google.golang.org/protobuf v1.26.0
)

//replace github.com/unistack-org/micro/v3 => ../../micro
