// Package intsettings holds the per-organization infra agent integration settings (endpoint, credentials) that
// are edited in the UI and delivered to agents through POST /v1/openlog/agent/sync (docs/contracts/api.md
// "Integration settings", releases-updates.md §3, postgres.md "Integration settings").
//
// Effective and Revision are shared by the management API and the sync endpoint, so both agree on what a host
// receives and on the revision the UI compares with the one the agent reports as applied.
package intsettings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// Integrations that accept remote settings.
const (
	Nginx      = "nginx"
	Redis      = "redis"
	MySQL      = "mysql"
	PostgreSQL = "postgresql"
	Docker     = "docker"
	MSSQL      = "mssql"
	IIS        = "iis"

	Apache        = "apache"
	Memcached     = "memcached"
	HAProxy       = "haproxy"
	RabbitMQ      = "rabbitmq"
	Elasticsearch = "elasticsearch"
	MongoDB       = "mongodb"
)

// RevisionDisabled is reported by agents configured with integrations.remote_config: false.
const RevisionDisabled = "disabled"

// Limits.
const (
	MaxHostIDBytes = 256
	MaxFieldLen    = 512
	MaxDatabases   = 64
)

// Errors.
var (
	ErrNotFound = errors.New("intsettings: not found")
	// ErrConflict means another setting has the same host scope, integration and match.
	ErrConflict = errors.New("intsettings: a setting for this integration, host and match already exists")
	// ErrNoSecretsKey means a password was given while OPENLOG_SECRETS_KEY is not configured.
	ErrNoSecretsKey = secrets.ErrNoKey
)

// ValidationError is an invalid request (400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalidf(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

type field uint8

const (
	fEndpoint field = 1 << iota
	fUsername
	fPassword
	fDatabase
	fDatabases
)

// allowedFields lists the settings each integration uses besides enabled and match.
var allowedFields = map[string]field{
	Nginx:      fEndpoint,
	Redis:      fEndpoint | fUsername | fPassword,
	MySQL:      fEndpoint | fUsername | fPassword,
	PostgreSQL: fEndpoint | fUsername | fPassword | fDatabase | fDatabases,
	Docker:     0,
	MSSQL:      fEndpoint | fUsername | fPassword,
	IIS:        0,

	Apache:        fEndpoint,
	Memcached:     fEndpoint,
	HAProxy:       fEndpoint,
	RabbitMQ:      fEndpoint | fUsername | fPassword,
	Elasticsearch: fEndpoint | fUsername | fPassword,
	// MongoDB's database is the authentication source, and the two database lists bound the dbStats reads.
	MongoDB: fEndpoint | fUsername | fPassword | fDatabase | fDatabases,
}

// urlEndpoints are the integrations whose endpoint is an http(s) URL — a status page or a management API —
// rather than host:port; the value is the example shown in the error message.
var urlEndpoints = map[string]string{
	Nginx:         "http://127.0.0.1/nginx_status",
	Apache:        "http://127.0.0.1/server-status?auto",
	HAProxy:       "http://127.0.0.1:8404/;csv",
	RabbitMQ:      "http://127.0.0.1:15672",
	Elasticsearch: "http://127.0.0.1:9200",
}

// socketEndpoints are the url integrations that also read a unix socket (HAProxy's runtime API).
var socketEndpoints = map[string]bool{HAProxy: true}

// Integrations returns the supported integration names, sorted.
func Integrations() []string {
	out := make([]string, 0, len(allowedFields))
	for k := range allowedFields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AllowsPassword reports whether integration uses a password.
func AllowsPassword(integration string) bool { return allowedFields[integration]&fPassword != 0 }

// Match selects the discovered service instances a setting applies to. The zero value matches every instance.
type Match struct {
	Port      *int   `json:"port"`
	Container string `json:"container"`
	Endpoint  string `json:"endpoint"`
	Instance  string `json:"instance"`
}

// IsZero reports whether no match field is set.
func (m Match) IsZero() bool {
	return m.Port == nil && m.Container == "" && m.Endpoint == "" && m.Instance == ""
}

func (m Match) equal(o Match) bool {
	samePort := (m.Port == nil) == (o.Port == nil) && (m.Port == nil || *m.Port == *o.Port)
	return samePort && m.Container == o.Container && m.Endpoint == o.Endpoint && m.Instance == o.Instance
}

// Setting is a stored integration setting. PasswordEnc is the ciphertext; it never leaves the server except
// decrypted in a sync answer to an agent of the organization.
type Setting struct {
	ID             string
	OrgID          string
	HostID         string // "" = every host of the organization
	Integration    string
	Match          Match
	Enabled        bool
	Endpoint       string
	Username       string
	PasswordEnc    string
	Database       string
	Databases      []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	UpdatedBy      string // user id, "" = unknown
	UpdatedByEmail string
}

// Input is the body of POST and PUT /api/v1/integrations/settings.
type Input struct {
	HostID      *string  `json:"host_id"`
	Integration string   `json:"integration"`
	Match       *Match   `json:"match"`
	Enabled     *bool    `json:"enabled"` // omitted = true
	Endpoint    string   `json:"endpoint"`
	Username    string   `json:"username"`
	Password    *string  `json:"password"` // omitted or null = keep (create: none), "" = clear, otherwise set
	Database    string   `json:"database"`
	Databases   []string `json:"databases"`
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func checkText(name, v string) error {
	if len(v) > MaxFieldLen {
		return invalidf("%s must be at most %d bytes", name, MaxFieldLen)
	}
	if hasControl(v) {
		return invalidf("%s must not contain control characters", name)
	}
	return nil
}

// normalize validates in and returns the setting it describes (without id, password and metadata).
func normalize(in Input) (Setting, error) {
	s := Setting{Integration: strings.TrimSpace(in.Integration), Enabled: in.Enabled == nil || *in.Enabled,
		Endpoint: strings.TrimSpace(in.Endpoint), Username: in.Username, Database: strings.TrimSpace(in.Database),
		Databases: []string{}}
	allowed, ok := allowedFields[s.Integration]
	if !ok {
		return s, invalidf("integration must be one of %s", strings.Join(Integrations(), ", "))
	}
	if in.HostID != nil {
		s.HostID = strings.TrimSpace(*in.HostID)
		if len(s.HostID) > MaxHostIDBytes || hasControl(s.HostID) {
			return s, invalidf("host_id must be at most %d bytes without control characters", MaxHostIDBytes)
		}
	}
	if m := in.Match; m != nil {
		s.Match = Match{Container: strings.TrimSpace(m.Container), Endpoint: strings.TrimSpace(m.Endpoint), Instance: strings.TrimSpace(m.Instance)}
		if m.Port != nil {
			if *m.Port < 1 || *m.Port > 65535 {
				return s, invalidf("match.port must be between 1 and 65535")
			}
			p := *m.Port
			s.Match.Port = &p
		}
		for name, v := range map[string]string{"match.container": s.Match.Container, "match.endpoint": s.Match.Endpoint, "match.instance": s.Match.Instance} {
			if err := checkText(name, v); err != nil {
				return s, err
			}
		}
	}
	for _, f := range []struct {
		name string
		bit  field
		set  bool
	}{
		{"endpoint", fEndpoint, s.Endpoint != ""},
		{"username", fUsername, s.Username != ""},
		{"password", fPassword, in.Password != nil && *in.Password != ""},
		{"database", fDatabase, s.Database != ""},
		{"databases", fDatabases, len(in.Databases) > 0},
	} {
		if f.set && allowed&f.bit == 0 {
			return s, invalidf("%s is not used by the %s integration", f.name, s.Integration)
		}
	}
	for name, v := range map[string]string{"endpoint": s.Endpoint, "username": s.Username, "database": s.Database} {
		if err := checkText(name, v); err != nil {
			return s, err
		}
	}
	if in.Password != nil && len(*in.Password) > MaxFieldLen {
		return s, invalidf("password must be at most %d bytes", MaxFieldLen)
	}
	if s.Endpoint != "" {
		if err := checkEndpoint(s.Integration, s.Endpoint); err != nil {
			return s, err
		}
	}
	if len(in.Databases) > MaxDatabases {
		return s, invalidf("databases must have at most %d entries", MaxDatabases)
	}
	for i, d := range in.Databases {
		d = strings.TrimSpace(d)
		if d == "" {
			return s, invalidf("databases[%d] must not be empty", i)
		}
		if err := checkText(fmt.Sprintf("databases[%d]", i), d); err != nil {
			return s, err
		}
		s.Databases = append(s.Databases, d)
	}
	return s, nil
}

func checkEndpoint(integration, ep string) error {
	example, isURL := urlEndpoints[integration]
	if isURL && !(socketEndpoints[integration] && strings.HasPrefix(ep, "unix:")) {
		u, err := url.Parse(ep)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
			return invalidf("endpoint must be an http(s) URL with a host (e.g. %s)", example)
		}
		return nil
	}
	if path, ok := strings.CutPrefix(ep, "unix:"); ok {
		if !strings.HasPrefix(path, "/") || len(path) < 2 {
			return invalidf("endpoint must be host:port or unix:/absolute/path")
		}
		return nil
	}
	host, port, err := net.SplitHostPort(ep)
	if err == nil {
		n, perr := strconv.Atoi(port)
		if host != "" && perr == nil && n >= 1 && n <= 65535 {
			return nil
		}
	}
	return invalidf("endpoint must be host:port or unix:/absolute/path")
}

// Validate checks an input without storing it.
func Validate(in Input) error {
	_, err := normalize(in)
	return err
}

func scopeRank(s Setting) int {
	r := 0
	if s.HostID != "" {
		r += 2
	}
	if !s.Match.IsZero() {
		r++
	}
	return r
}

// Sort orders settings as the API lists them: all-hosts rows first, then per host id; within a scope rows
// without a match first; ties by created_at, then id.
func Sort(ss []Setting) {
	sort.SliceStable(ss, func(i, j int) bool {
		a, b := ss[i], ss[j]
		if (a.HostID == "") != (b.HostID == "") {
			return a.HostID == ""
		}
		if a.HostID != b.HostID {
			return a.HostID < b.HostID
		}
		if ra, rb := scopeRank(a), scopeRank(b); ra != rb {
			return ra < rb
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

// Effective returns the settings that apply to hostID, in the order the agent applies them: all-hosts rows
// first, then rows for the host; within each scope rows without a match first; ties by created_at, then id.
func Effective(ss []Setting, hostID string) []Setting {
	out := make([]Setting, 0, len(ss))
	for _, s := range ss {
		if s.HostID == "" || s.HostID == hostID {
			out = append(out, s)
		}
	}
	Sort(out)
	return out
}

// AgentMatch is the match of an agent item.
type AgentMatch struct {
	Port      *int   `json:"port,omitempty"`
	Container string `json:"container"`
	Endpoint  string `json:"endpoint"`
	Instance  string `json:"instance"`
}

// AgentItem is one entry of integrations_config.items in the sync answer.
type AgentItem struct {
	Integration string      `json:"integration"`
	Match       *AgentMatch `json:"match,omitempty"`
	Enabled     bool        `json:"enabled"`
	Endpoint    string      `json:"endpoint"`
	Username    string      `json:"username"`
	Password    string      `json:"password"`
	Database    string      `json:"database"`
	Databases   []string    `json:"databases"`
}

func agentItem(s Setting, password string) AgentItem {
	it := AgentItem{Integration: s.Integration, Enabled: s.Enabled, Endpoint: s.Endpoint, Username: s.Username,
		Password: password, Database: s.Database, Databases: s.Databases}
	if it.Databases == nil {
		it.Databases = []string{}
	}
	if !s.Match.IsZero() {
		it.Match = &AgentMatch{Port: s.Match.Port, Container: s.Match.Container, Endpoint: s.Match.Endpoint, Instance: s.Match.Instance}
	}
	return it
}

// Revision identifies effective settings: "sha256:" + hex(sha256(JSON of the agent items with each password
// replaced by its stored ciphertext)). It needs no key and changes with every change, including a new password.
// An empty list has a revision too (the hash of "[]": no remote config).
func Revision(effective []Setting) string {
	items := make([]AgentItem, 0, len(effective))
	for _, s := range effective {
		items = append(items, agentItem(s, s.PasswordEnc))
	}
	b, _ := json.Marshal(items)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// PasswordAAD binds a password ciphertext to its organization and setting.
func PasswordAAD(orgID, settingID string) string { return orgID + "/" + settingID }

// DecryptError reports the setting whose password cannot be decrypted (the error never contains secrets).
type DecryptError struct {
	SettingID string
	Err       error
}

func (e *DecryptError) Error() string { return "setting " + e.SettingID + ": " + e.Err.Error() }
func (e *DecryptError) Unwrap() error { return e.Err }

// AgentItems builds the agent items of effective settings with decrypted passwords.
func AgentItems(kr *secrets.Keyring, effective []Setting) ([]AgentItem, error) {
	items := make([]AgentItem, 0, len(effective))
	for _, s := range effective {
		pw := ""
		if s.PasswordEnc != "" {
			b, err := kr.Decrypt(s.PasswordEnc, PasswordAAD(s.OrgID, s.ID))
			if err != nil {
				return nil, &DecryptError{SettingID: s.ID, Err: err}
			}
			pw = string(b)
		}
		items = append(items, agentItem(s, pw))
	}
	return items, nil
}
