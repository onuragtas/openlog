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
	github.com/microsoft/go-mssqldb v1.11.0
	github.com/shirou/gopsutil/v4 v4.26.8
	github.com/yusufpapurcu/wmi v1.2.4
	golang.org/x/sys v0.47.0
	google.golang.org/protobuf v1.36.12
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/ebitengine/purego v0.10.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

// libs/release is developed in the same repository (Apache-2.0). Builds need the repository root
// as context (see Dockerfile and Makefile).
replace github.com/onuragtas/openlog/libs/release => ../../libs/release
