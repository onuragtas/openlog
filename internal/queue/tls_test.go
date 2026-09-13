package queue

import (
	"crypto/tls"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/testutil/testcerts"
)

func TestClientOptionsDefaultPlaintext(t *testing.T) {
	opts, err := ClientOptions(config.Common{})
	if err != nil || len(opts) != 0 {
		t.Fatalf("default options = %d, %v; want none", len(opts), err)
	}
}

func TestClientOptionsTLSAndSASL(t *testing.T) {
	b, err := testcerts.Generate(t.TempDir(), "kafka")
	if err != nil {
		t.Fatal(err)
	}
	for _, mech := range []string{config.SASLPlain, config.SASLScramSHA256, config.SASLScramSHA512} {
		t.Run(mech, func(t *testing.T) {
			c := config.Common{
				KafkaTLS:  config.TLS{Enabled: true, CAFile: b.CAFile, CertFile: b.ClientCert, KeyFile: b.ClientKey},
				KafkaSASL: config.KafkaSASL{Mechanism: mech, Username: "openlog", Password: "pw"},
			}
			opts, err := ClientOptions(c)
			if err != nil {
				t.Fatal(err)
			}
			cl, err := kgo.NewClient(append([]kgo.Opt{kgo.SeedBrokers("kafka:9093")}, opts...)...)
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			tc, _ := cl.OptValue(kgo.DialTLSConfig).(*tls.Config)
			if tc == nil || tc.RootCAs == nil || len(tc.Certificates) != 1 {
				t.Errorf("dial TLS config = %+v", tc)
			}
			mechs, _ := cl.OptValue(kgo.SASL).([]sasl.Mechanism)
			if len(mechs) != 1 || mechs[0].Name() != mech {
				t.Errorf("SASL mechanisms = %v, want %s", mechs, mech)
			}
		})
	}
}

func TestClientOptionsBadCA(t *testing.T) {
	_, err := ClientOptions(config.Common{KafkaTLS: config.TLS{Enabled: true, CAFile: "/nonexistent/ca.crt"}})
	if err == nil {
		t.Fatal("missing CA file accepted")
	}
}
