package clickhouse

import (
	"context"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/testutil/testcerts"
)

// Direct shard connections (D-018) must use the same TLS settings as the bootstrap connection.
func TestShardPoolsUseTLS(t *testing.T) {
	b, err := testcerts.Generate(t.TempDir(), "chi-openlog-0-0")
	if err != nil {
		t.Fatal(err)
	}
	c := config.Common{
		ClickHouseAddr: []string{"clickhouse:9440"}, ClickHouseUser: "openlog",
		ClickHouseTLS: config.TLS{Enabled: true, CAFile: b.CAFile, CertFile: b.ClientCert, KeyFile: b.ClientKey},
	}
	o := OptionsFromConfig(c)
	o.Addr = []string{"chi-openlog-0-0:9440"}
	co, err := lazyOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	// TLS is dialed by config.TLSReloader (certificate reload, D-048), which verifies each dialed replica host.
	if co.DialContext == nil || co.TLS != nil {
		t.Fatalf("replica pool: DialContext set %v, TLS %+v", co.DialContext != nil, co.TLS)
	}

	plain, err := lazyOptions(OptionsFromConfig(config.Common{}))
	if err != nil || plain.TLS != nil || plain.DialContext != nil {
		t.Errorf("plaintext replica pool: TLS %+v dial %v err %v", plain.TLS, plain.DialContext != nil, err)
	}
}

func TestOpenReportsTLSConfigErrors(t *testing.T) {
	o := Options{Addr: []string{"127.0.0.1:1"}, TLS: config.TLS{Enabled: true, CAFile: "/nonexistent/ca.crt"}}
	if _, err := Open(context.Background(), o); err == nil || !strings.Contains(err.Error(), "clickhouse tls") {
		t.Errorf("Open: %v", err)
	}
	if _, err := openLazy(o); err == nil || !strings.Contains(err.Error(), "clickhouse tls") {
		t.Errorf("openLazy: %v", err)
	}
}
