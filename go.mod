module github.com/unistack-org/micro-server-grpc/v3

go 1.15

require (
	github.com/golang/protobuf v1.4.3
	github.com/google/go-cmp v0.5.1 // indirect
	github.com/unistack-org/micro/v3 v3.2.14
	golang.org/x/net v0.0.0-20210224082022-3d97a244fca7
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/genproto v0.0.0-20200904004341-0bd0a958aa1d // indirect
	google.golang.org/grpc v1.35.0
	google.golang.org/protobuf v1.25.0
)

//replace github.com/unistack-org/micro/v3 => ../../micro
