// Package testcerts generates throw-away PKI material for tests: a CA, a server
// certificate for the given names, a client certificate and an unrelated CA
// ("wrong CA"). Nothing is committed; every run creates new keys.
package testcerts

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Bundle lists the PEM files written by Generate. Keys are unencrypted PKCS#8.
type Bundle struct {
	Dir        string
	CAFile     string // trusted CA
	ServerCert string // server certificate (SANs: the names given to Generate)
	ServerKey  string
	ClientCert string // client certificate signed by the same CA (CN "openlog")
	ClientKey  string
	WrongCA    string // an unrelated CA that did not sign anything here
	// Combined holds server key + certificate + CA in one file (some servers want one PEM).
	Combined string
	// ClientCombined holds client key + certificate + CA (e.g. a Kafka PEM keystore for CLI tools).
	ClientCombined string
}

// Generate writes a bundle into dir. names are DNS names or IP addresses for the server certificate.
func Generate(dir string, names ...string) (*Bundle, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b := &Bundle{
		Dir: dir, CAFile: filepath.Join(dir, "ca.crt"), ServerCert: filepath.Join(dir, "server.crt"), ServerKey: filepath.Join(dir, "server.key"),
		ClientCert: filepath.Join(dir, "client.crt"), ClientKey: filepath.Join(dir, "client.key"), WrongCA: filepath.Join(dir, "wrong-ca.crt"),
		Combined: filepath.Join(dir, "server.pem"), ClientCombined: filepath.Join(dir, "client.pem"),
	}
	caKey, caCert, caDER, err := newCA("openlog test CA")
	if err != nil {
		return nil, err
	}
	// The server certificate is also valid for client auth: brokers and ClickHouse servers use it
	// when they connect to each other (inter-broker mTLS, Distributed queries over the secure port).
	srvKey, srvDER, err := leaf(caKey, caCert, "openlog test server", names, x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, err
	}
	cliKey, cliDER, err := leaf(caKey, caCert, "openlog", nil, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, err
	}
	_, _, wrongDER, err := newCA("unrelated test CA")
	if err != nil {
		return nil, err
	}
	srvKeyPEM, err := keyPEM(srvKey)
	if err != nil {
		return nil, err
	}
	cliKeyPEM, err := keyPEM(cliKey)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{
		b.CAFile:         certPEM(caDER),
		b.ServerCert:     certPEM(srvDER),
		b.ServerKey:      srvKeyPEM,
		b.ClientCert:     certPEM(cliDER),
		b.ClientKey:      cliKeyPEM,
		b.WrongCA:        certPEM(wrongDER),
		b.Combined:       append(append(append([]byte{}, srvKeyPEM...), certPEM(srvDER)...), certPEM(caDER)...),
		b.ClientCombined: append(append(append([]byte{}, cliKeyPEM...), certPEM(cliDER)...), certPEM(caDER)...),
	}
	for name, data := range files {
		// World-readable on purpose: containers read them under their own uid. Test keys only.
		if err := os.WriteFile(name, data, 0o644); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func newCA(cn string) (*rsa.PrivateKey, *x509.Certificate, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"openlog tests"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(7 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return key, cert, der, err
}

func leaf(caKey *rsa.PrivateKey, ca *x509.Certificate, cn string, names []string, usage ...x509.ExtKeyUsage) (*rsa.PrivateKey, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"openlog tests"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usage,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("sign %s: %w", cn, err)
	}
	return key, der, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	return n
}

func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func keyPEM(k *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}
