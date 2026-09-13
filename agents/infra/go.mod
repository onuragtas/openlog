module github.com/onuragtas/openlog/agents/infra

go 1.26

require (
	github.com/onuragtas/openlog/libs/release v0.0.0
	go.opentelemetry.io/proto/otlp v1.11.0
	gopkg.in/yaml.v3 v3.0.1
)

require google.golang.org/protobuf v1.36.12

// libs/release is developed in the same repository (Apache-2.0). Builds need the repository root
// as context (see Dockerfile and Makefile).
replace github.com/onuragtas/openlog/libs/release => ../../libs/release
