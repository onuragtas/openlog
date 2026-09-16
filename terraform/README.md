# terraform-provider-openlog

Terraform provider for openlog: alert rules, notification channels, routing rules, service level objectives
and dashboards as code (D-131).

Its own Go module (`github.com/onuragtas/openlog/terraform`) under **Apache-2.0**, like the agents and SDKs
(D-004): the Terraform Plugin Framework's dependency tree stays out of the AGPL-3.0 server binaries of the
root module.

```
cmd/terraform-provider-openlog/   the plugin binary
internal/client/                  HTTP client of /api/v1 (auth, error shape, CRUD per resource)
internal/provider/                provider block, resources, data sources
```

Operator documentation, a worked example and the prerequisites — above all an API key that may **write**, which
openlog does not issue yet — are in [docs/operations/terraform.md](../docs/operations/terraform.md).

```sh
go build ./cmd/terraform-provider-openlog
go test ./...
```
