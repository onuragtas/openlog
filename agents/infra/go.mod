module github.com/onuragtas/openlog/agents/infra

go 1.26

require (
	github.com/onuragtas/openlog/libs/release v0.0.0
	go.opentelemetry.io/proto/otlp v1.11.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/go-sql-driver/mysql v1.10.1
	github.com/godbus/dbus/v5 v5.2.2
	github.com/jackc/pgx/v5 v5.11.0
	google.golang.org/protobuf v1.36.12
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

// libs/release is developed in the same repository (Apache-2.0). Builds need the repository root
// as context (see Dockerfile and Makefile).
replace github.com/onuragtas/openlog/libs/release => ../../libs/release
