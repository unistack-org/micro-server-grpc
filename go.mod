module github.com/unistack-org/micro-server-grpc

go 1.15

require (
	github.com/golang/protobuf v1.4.3
	github.com/google/go-cmp v0.5.1 // indirect
	github.com/unistack-org/micro/v3 v3.0.2-0.20201215085205-f14efa64f09f
	golang.org/x/net v0.0.0-20200904194848-62affa334b73
	golang.org/x/sys v0.0.0-20200803210538-64077c9b5642 // indirect
	golang.org/x/text v0.3.3 // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/genproto v0.0.0-20200904004341-0bd0a958aa1d // indirect
	google.golang.org/grpc v1.31.1
	google.golang.org/protobuf v1.25.0
)

//replace github.com/unistack-org/micro/v3 => ../../micro
