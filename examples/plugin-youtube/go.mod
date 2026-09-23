module github.com/metruzanca/nanoflux/examples/plugin-youtube

go 1.26.6

require (
	github.com/hashicorp/go-plugin v1.6.3
	github.com/metruzanca/nanoflux v0.0.0
	github.com/metruzanca/nanoflux/pluginapi v0.0.0
)

require (
	github.com/fatih/color v1.19.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v0.14.1 // indirect
	github.com/hashicorp/yamux v0.1.1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mmcdole/gofeed v1.4.2 // indirect
	github.com/mmcdole/goxpp/v2 v2.0.0 // indirect
	github.com/oklog/run v1.0.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240903143218-8af14fe29dc1 // indirect
	google.golang.org/grpc v1.68.0 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)

replace github.com/metruzanca/nanoflux => ../../

replace github.com/metruzanca/nanoflux/pluginapi => ../../pluginapi
