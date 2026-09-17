// Package cloudconnect collects metrics of managed cloud services (D-135): an organization stores one
// connection per cloud account in PostgreSQL (migrations/postgres/0092_cloud_connections.sql), the api leader
// polls the due ones and writes every data point through the same path as agent metrics, so RDS, Azure SQL or
// Cloud SQL appear in the Metrics Explorer, in alert rules and on dashboards without a new query path.
//
// Layout: cloudconnect.go (model, validation, service catalog), provider.go (the provider contract and the
// mapping to cloud.* attributes), aws.go / azure.go / gcp.go (the provider clients), transport.go (shared
// HTTP, throttling and backoff), sigv4.go (AWS request signing), collector.go (one poll of one scope with its
// guardrails), scheduler.go (leader task, due selection, concurrency limits), metrics.go (the ClickHouse
// writer), pgstore.go (PostgreSQL store).
//
// The shape follows internal/synthetics: definitions and the last outcome in PostgreSQL, the scheduler claims
// due rows with FOR UPDATE ... SKIP LOCKED, and the telemetry is mirrored into `metrics` so nothing about
// alerting or dashboards has to know that these points came from a cloud API.
package cloudconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Providers openlog can read metrics from.
const (
	ProviderAWS   = "aws"
	ProviderAzure = "azure"
	ProviderGCP   = "gcp"
)

// Providers returns the supported providers in display order.
func Providers() []string { return []string{ProviderAWS, ProviderAzure, ProviderGCP} }

// Ingest modes. Only poll exists; push is the extension point for provider-side delivery (Kinesis Firehose,
// Event Hubs, Pub/Sub), which reuses the same row — credentials, scopes and services stay meaningful and a
// pushing connection simply has no schedule rows (D-135).
const (
	IngestPoll = "poll"
	IngestPush = "push"
)

// Outcome of one poll of one scope.
const (
	// StatusOK means every requested service answered.
	StatusOK = "ok"
	// StatusPartial means some services failed, or a guardrail stopped the run before it finished.
	StatusPartial = "partial"
	// StatusError means nothing was collected for this scope.
	StatusError = "error"
)

// Limits of a definition and of one poll. The database repeats the bounds as CHECK constraints.
const (
	MaxNameRunes = 200
	MaxScopes    = 50
	MaxScopeLen  = 200
	MaxServices  = 50
	MaxPerOrg    = 50

	MinIntervalSecs = 60
	MaxIntervalSecs = 86400
	DefaultInterval = 300

	MinMetricsPerPoll     = 100
	MaxMetricsPerPoll     = 200000
	DefaultMetricsPerPoll = 5000

	MinAPICallsPerPoll     = 1
	MaxAPICallsPerPoll     = 5000
	DefaultAPICallsPerPoll = 200

	// MaxErrorBytes bounds a stored error message (it is shown next to the connection).
	MaxErrorBytes = 512
	// MaxCredentialBytes bounds one credential field (a GCP private key is the largest, ~1.7 KiB).
	MaxCredentialBytes = 8192
	// MaxRunsPerConnection is how many polls the history keeps per connection; older rows are pruned on write.
	MaxRunsPerConnection = 200
	// MaxRunsPerResponse bounds the history endpoint.
	MaxRunsPerResponse = 200
)

var (
	// ErrNotFound is returned for an unknown connection of the organization.
	ErrNotFound = errors.New("cloud connection not found")
	// ErrLimit reports that the organization already has MaxPerOrg connections.
	ErrLimit = errors.New("cloud connection limit reached")
	// ErrNoCredentials reports a connection whose credentials were never stored (or could not be decrypted).
	ErrNoCredentials = errors.New("the connection has no stored credentials")
)

// ValidationError is an invalid definition (400 invalid_argument with the field path).
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

func invalid(field, msg string) error { return &ValidationError{Field: field, Msg: msg} }

// Actor is who changed a definition (audit log): a signed-in user, or an API key acting with its own role
// (D-133), in which case UserID and Email are empty.
type Actor struct {
	UserID     string
	Email      string
	IP         string
	APIKeyID   string
	APIKeyName string
}

// ---- credentials ----

// Credentials are the provider secrets of one connection. They are stored as one encrypted JSON document
// (AES-256-GCM with OPENLOG_SECRETS_KEY, AAD org_id/id) and never leave the server: the API returns
// credentials_set, never a value, and no field of this struct is written to a log or an audit detail.
type Credentials struct {
	// AWS: static IAM keys. SessionToken is optional (temporary credentials).
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`

	// Azure: a service principal with the Monitoring Reader role on the subscription.
	TenantID     string `json:"tenant_id,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`

	// GCP: a service account key with roles/monitoring.viewer. PrivateKey is the PEM of the key file's
	// private_key field; TokenURI defaults to Google's token endpoint.
	ClientEmail string `json:"client_email,omitempty"`
	PrivateKey  string `json:"private_key,omitempty"`
	TokenURI    string `json:"token_uri,omitempty"`
}

// IsZero reports whether no credential field is set.
func (c Credentials) IsZero() bool { return c == Credentials{} }

// Encode renders the credentials as the JSON document that is encrypted at rest.
func (c Credentials) Encode() ([]byte, error) { return json.Marshal(c) }

// CredentialsAAD binds a credential ciphertext to its organization and connection, so a value copied into
// another organization's row does not decrypt (the same rule as intsettings.PasswordAAD).
func CredentialsAAD(orgID, connectionID string) string { return orgID + "/" + connectionID }

// DecodeCredentials parses the stored JSON document.
func DecodeCredentials(b []byte) (Credentials, error) {
	var c Credentials
	err := json.Unmarshal(b, &c)
	return c, err
}

// credentialFields lists the fields a provider uses, in form order, with whether they are required and
// whether they are secret (the UI renders secrets as password inputs and never prefills them).
type credentialField struct {
	Key      string
	Required bool
	Secret   bool
}

var credentialFields = map[string][]credentialField{
	ProviderAWS: {
		{Key: "access_key_id", Required: true},
		{Key: "secret_access_key", Required: true, Secret: true},
		{Key: "session_token", Secret: true},
	},
	ProviderAzure: {
		{Key: "tenant_id", Required: true},
		{Key: "client_id", Required: true},
		{Key: "client_secret", Required: true, Secret: true},
	},
	ProviderGCP: {
		{Key: "client_email", Required: true},
		{Key: "private_key", Required: true, Secret: true},
		{Key: "token_uri"},
	},
}

// CredentialField is one input of the provider catalog (GET /api/v1/cloud/providers).
type CredentialField struct {
	Key      string
	Required bool
	Secret   bool
}

// CredentialFields returns the credential inputs of a provider, in form order.
func CredentialFields(provider string) []CredentialField {
	out := make([]CredentialField, 0, len(credentialFields[provider]))
	for _, f := range credentialFields[provider] {
		out = append(out, CredentialField{Key: f.Key, Required: f.Required, Secret: f.Secret})
	}
	return out
}

// field returns the value of a credential field by its API key.
func (c Credentials) field(key string) string {
	switch key {
	case "access_key_id":
		return c.AccessKeyID
	case "secret_access_key":
		return c.SecretAccessKey
	case "session_token":
		return c.SessionToken
	case "tenant_id":
		return c.TenantID
	case "client_id":
		return c.ClientID
	case "client_secret":
		return c.ClientSecret
	case "client_email":
		return c.ClientEmail
	case "private_key":
		return c.PrivateKey
	case "token_uri":
		return c.TokenURI
	}
	return ""
}

// Validate checks that the credentials carry what the provider needs and nothing it does not use, so a
// connection cannot be saved with secrets that are silently ignored.
func (c Credentials) Validate(provider string) error {
	fields, ok := credentialFields[provider]
	if !ok {
		return invalid("provider", "must be one of "+strings.Join(Providers(), ", "))
	}
	used := map[string]bool{}
	for _, f := range fields {
		used[f.Key] = true
		v := c.field(f.Key)
		if f.Required && strings.TrimSpace(v) == "" {
			return invalid("credentials."+f.Key, "required for a "+provider+" connection")
		}
		if len(v) > MaxCredentialBytes {
			return invalid("credentials."+f.Key, fmt.Sprintf("must be at most %d bytes", MaxCredentialBytes))
		}
	}
	for _, key := range allCredentialKeys {
		if !used[key] && strings.TrimSpace(c.field(key)) != "" {
			return invalid("credentials."+key, "is not used by a "+provider+" connection")
		}
	}
	return nil
}

// allCredentialKeys is every credential field of every provider, so Validate can reject the ones a provider
// does not use.
var allCredentialKeys = []string{"access_key_id", "secret_access_key", "session_token", "tenant_id",
	"client_id", "client_secret", "client_email", "private_key", "token_uri"}

// ---- service catalog ----

// Metric is one metric of a managed service.
type Metric struct {
	// Name is the provider's metric name. It is sent in the API request and kept on every data point as the
	// cloud.metric.name attribute, so nothing of the provider's naming is lost by openlog's own name.
	Name string
	// Unit is the OTLP unit of the value ("1", "By", "ms", "s", "{count}").
	Unit string
	// Stat is the aggregation asked of the provider: a CloudWatch statistic (Average, Sum, Maximum) or an
	// Azure Monitor aggregation. GCP aligns server-side and ignores it.
	Stat string
}

// Service is one managed service openlog can collect.
type Service struct {
	// ID is the value stored in cloud_connections.services and used in openlog's metric names.
	ID string
	// Provider owning the service.
	Provider string
	// Namespace is the provider's grouping: a CloudWatch namespace, an Azure resource type, or the metric
	// type prefix of GCP Cloud Monitoring.
	Namespace string
	// Platform is the cloud.platform attribute of every data point (semantic-conventions §1).
	Platform string
	// ResourceDimension is the provider dimension naming the individual resource; its value becomes the
	// cloud.resource.name attribute (AWS CloudWatch; empty where the provider names the resource itself).
	ResourceDimension string
	// ResourceType is the GCP monitored resource type (unused for AWS and Azure).
	ResourceType string
	// PeriodSeconds is the aggregation period asked of the provider; 0 derives it from the poll window.
	PeriodSeconds int
	// LookbackSeconds widens the window for metrics a provider publishes rarely (S3 storage is written once
	// a day, so a five-minute window would always come back empty); 0 uses the collector's window.
	LookbackSeconds int
	Metrics         []Metric
}

// catalog is the built-in service catalog. It is deliberately a list in code, not a table: adding a service
// is a code change with a metric name contract (docs/contracts/semantic-conventions.md §9), not configuration.
var catalog = []Service{
	// ---- AWS (CloudWatch) ----
	{ID: "rds", Provider: ProviderAWS, Namespace: "AWS/RDS", Platform: "aws_rds", ResourceDimension: "DBInstanceIdentifier", Metrics: []Metric{
		{Name: "CPUUtilization", Unit: "1", Stat: "Average"},
		{Name: "DatabaseConnections", Unit: "{connection}", Stat: "Average"},
		{Name: "FreeStorageSpace", Unit: "By", Stat: "Average"},
		{Name: "FreeableMemory", Unit: "By", Stat: "Average"},
		{Name: "ReadLatency", Unit: "s", Stat: "Average"},
		{Name: "WriteLatency", Unit: "s", Stat: "Average"},
		{Name: "ReadIOPS", Unit: "{operation}/s", Stat: "Average"},
		{Name: "WriteIOPS", Unit: "{operation}/s", Stat: "Average"},
	}},
	// S3 storage metrics are published once a day, so they are read with a daily period over a two-day
	// window; a five-minute window would always come back empty.
	{ID: "s3", Provider: ProviderAWS, Namespace: "AWS/S3", Platform: "aws_s3", ResourceDimension: "BucketName",
		PeriodSeconds: 86400, LookbackSeconds: 2 * 86400, Metrics: []Metric{
			{Name: "BucketSizeBytes", Unit: "By", Stat: "Average"},
			{Name: "NumberOfObjects", Unit: "{object}", Stat: "Average"},
		}},
	{ID: "lambda", Provider: ProviderAWS, Namespace: "AWS/Lambda", Platform: "aws_lambda", ResourceDimension: "FunctionName", Metrics: []Metric{
		{Name: "Invocations", Unit: "{invocation}", Stat: "Sum"},
		{Name: "Errors", Unit: "{error}", Stat: "Sum"},
		{Name: "Throttles", Unit: "{throttle}", Stat: "Sum"},
		{Name: "Duration", Unit: "ms", Stat: "Average"},
		{Name: "ConcurrentExecutions", Unit: "{execution}", Stat: "Maximum"},
	}},
	{ID: "sqs", Provider: ProviderAWS, Namespace: "AWS/SQS", Platform: "aws_sqs", ResourceDimension: "QueueName", Metrics: []Metric{
		{Name: "ApproximateNumberOfMessagesVisible", Unit: "{message}", Stat: "Average"},
		{Name: "ApproximateAgeOfOldestMessage", Unit: "s", Stat: "Maximum"},
		{Name: "NumberOfMessagesSent", Unit: "{message}", Stat: "Sum"},
		{Name: "NumberOfMessagesDeleted", Unit: "{message}", Stat: "Sum"},
	}},
	{ID: "dynamodb", Provider: ProviderAWS, Namespace: "AWS/DynamoDB", Platform: "aws_dynamodb", ResourceDimension: "TableName", Metrics: []Metric{
		{Name: "ConsumedReadCapacityUnits", Unit: "{unit}", Stat: "Sum"},
		{Name: "ConsumedWriteCapacityUnits", Unit: "{unit}", Stat: "Sum"},
		{Name: "ThrottledRequests", Unit: "{request}", Stat: "Sum"},
		{Name: "SuccessfulRequestLatency", Unit: "ms", Stat: "Average"},
	}},
	{ID: "elb", Provider: ProviderAWS, Namespace: "AWS/ApplicationELB", Platform: "aws_elb", ResourceDimension: "LoadBalancer", Metrics: []Metric{
		{Name: "RequestCount", Unit: "{request}", Stat: "Sum"},
		{Name: "TargetResponseTime", Unit: "s", Stat: "Average"},
		{Name: "HTTPCode_Target_5XX_Count", Unit: "{response}", Stat: "Sum"},
		{Name: "HTTPCode_ELB_5XX_Count", Unit: "{response}", Stat: "Sum"},
	}},
	{ID: "elasticache", Provider: ProviderAWS, Namespace: "AWS/ElastiCache", Platform: "aws_elasticache", ResourceDimension: "CacheClusterId", Metrics: []Metric{
		{Name: "CPUUtilization", Unit: "1", Stat: "Average"},
		{Name: "DatabaseMemoryUsagePercentage", Unit: "1", Stat: "Average"},
		{Name: "CacheHits", Unit: "{hit}", Stat: "Sum"},
		{Name: "CacheMisses", Unit: "{miss}", Stat: "Sum"},
		{Name: "Evictions", Unit: "{eviction}", Stat: "Sum"},
	}},

	// ---- Azure (Azure Monitor) ----
	{ID: "azure_sql", Provider: ProviderAzure, Namespace: "Microsoft.Sql/servers/databases", Platform: "azure_sql", Metrics: []Metric{
		{Name: "cpu_percent", Unit: "1", Stat: "Average"},
		{Name: "storage_percent", Unit: "1", Stat: "Average"},
		{Name: "dtu_consumption_percent", Unit: "1", Stat: "Average"},
		{Name: "connection_successful", Unit: "{connection}", Stat: "Total"},
		{Name: "connection_failed", Unit: "{connection}", Stat: "Total"},
		{Name: "deadlock", Unit: "{deadlock}", Stat: "Total"},
	}},
	{ID: "azure_storage", Provider: ProviderAzure, Namespace: "Microsoft.Storage/storageAccounts", Platform: "azure_storage", Metrics: []Metric{
		{Name: "UsedCapacity", Unit: "By", Stat: "Average"},
		{Name: "Transactions", Unit: "{transaction}", Stat: "Total"},
		{Name: "SuccessE2ELatency", Unit: "ms", Stat: "Average"},
		{Name: "Availability", Unit: "1", Stat: "Average"},
	}},
	{ID: "azure_vm", Provider: ProviderAzure, Namespace: "Microsoft.Compute/virtualMachines", Platform: "azure_vm", Metrics: []Metric{
		{Name: "Percentage CPU", Unit: "1", Stat: "Average"},
		{Name: "Available Memory Bytes", Unit: "By", Stat: "Average"},
		{Name: "Disk Read Bytes", Unit: "By", Stat: "Total"},
		{Name: "Disk Write Bytes", Unit: "By", Stat: "Total"},
		{Name: "Network In Total", Unit: "By", Stat: "Total"},
		{Name: "Network Out Total", Unit: "By", Stat: "Total"},
	}},
	{ID: "azure_functions", Provider: ProviderAzure, Namespace: "Microsoft.Web/sites", Platform: "azure_functions", Metrics: []Metric{
		{Name: "FunctionExecutionCount", Unit: "{execution}", Stat: "Total"},
		{Name: "FunctionExecutionUnits", Unit: "{unit}", Stat: "Total"},
		{Name: "Requests", Unit: "{request}", Stat: "Total"},
		{Name: "Http5xx", Unit: "{response}", Stat: "Total"},
	}},
	{ID: "azure_cosmos", Provider: ProviderAzure, Namespace: "Microsoft.DocumentDB/databaseAccounts", Platform: "azure_cosmos", Metrics: []Metric{
		{Name: "TotalRequests", Unit: "{request}", Stat: "Total"},
		{Name: "TotalRequestUnits", Unit: "{unit}", Stat: "Total"},
		{Name: "ServerSideLatency", Unit: "ms", Stat: "Average"},
	}},

	// ---- GCP (Cloud Monitoring) ----
	{ID: "cloud_sql", Provider: ProviderGCP, Namespace: "cloudsql.googleapis.com/database", Platform: "gcp_cloud_sql", ResourceType: "cloudsql_database", Metrics: []Metric{
		{Name: "cpu/utilization", Unit: "1", Stat: "Average"},
		{Name: "memory/utilization", Unit: "1", Stat: "Average"},
		{Name: "disk/utilization", Unit: "1", Stat: "Average"},
		{Name: "network/connections", Unit: "{connection}", Stat: "Average"},
	}},
	{ID: "gcs", Provider: ProviderGCP, Namespace: "storage.googleapis.com", Platform: "gcp_cloud_storage", ResourceType: "gcs_bucket", Metrics: []Metric{
		{Name: "storage/total_bytes", Unit: "By", Stat: "Average"},
		{Name: "storage/object_count", Unit: "{object}", Stat: "Average"},
		{Name: "api/request_count", Unit: "{request}", Stat: "Sum"},
	}},
	{ID: "cloud_functions", Provider: ProviderGCP, Namespace: "cloudfunctions.googleapis.com/function", Platform: "gcp_cloud_functions", ResourceType: "cloud_function", Metrics: []Metric{
		{Name: "execution_count", Unit: "{execution}", Stat: "Sum"},
		{Name: "execution_times", Unit: "ns", Stat: "Average"},
		{Name: "active_instances", Unit: "{instance}", Stat: "Average"},
	}},
	{ID: "pubsub", Provider: ProviderGCP, Namespace: "pubsub.googleapis.com/subscription", Platform: "gcp_pubsub", ResourceType: "pubsub_subscription", Metrics: []Metric{
		{Name: "num_undelivered_messages", Unit: "{message}", Stat: "Average"},
		{Name: "oldest_unacked_message_age", Unit: "s", Stat: "Maximum"},
	}},
	{ID: "gce", Provider: ProviderGCP, Namespace: "compute.googleapis.com/instance", Platform: "gcp_compute_engine", ResourceType: "gce_instance", Metrics: []Metric{
		{Name: "cpu/utilization", Unit: "1", Stat: "Average"},
		{Name: "network/received_bytes_count", Unit: "By", Stat: "Sum"},
		{Name: "network/sent_bytes_count", Unit: "By", Stat: "Sum"},
	}},
}

// Services returns the services of a provider, in catalog order.
func Services(provider string) []Service {
	out := []Service{}
	for _, s := range catalog {
		if s.Provider == provider {
			out = append(out, s)
		}
	}
	return out
}

// ServiceByID returns one service of a provider.
func ServiceByID(provider, id string) (Service, bool) {
	for _, s := range catalog {
		if s.Provider == provider && s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

// serviceIDs returns the service ids of a provider (for error messages).
func serviceIDs(provider string) []string {
	out := []string{}
	for _, s := range Services(provider) {
		out = append(out, s.ID)
	}
	return out
}

// ---- model ----

// Input is the writable part of a connection.
type Input struct {
	Name                string   `json:"name"`
	Provider            string   `json:"provider"`
	IngestMode          string   `json:"ingest_mode"`
	Enabled             bool     `json:"enabled"`
	Scopes              []string `json:"scopes"`
	Services            []string `json:"services"`
	PollIntervalSeconds int      `json:"poll_interval_seconds"`
	MaxMetricsPerPoll   int      `json:"max_metrics_per_poll"`
	MaxAPICallsPerPoll  int      `json:"max_api_calls_per_poll"`
	// Credentials is nil on an update to keep the stored ones; a value replaces them. It is never part of a
	// response.
	Credentials *Credentials `json:"credentials"`
}

// Connection is a stored definition with the schedule state of every scope.
type Connection struct {
	ID    string
	OrgID string
	Input
	// CredentialsSet reports whether credentials are stored; the values never leave the server.
	CredentialsSet   bool
	CredentialsKeyID string
	CreatedByEmail   string
	UpdatedByEmail   string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// Status is the schedule row per scope (the last poll and when the next one is due).
	Status []ScopeStatus
}

// ScopeStatus is what the scheduler recorded for one connection and scope.
type ScopeStatus struct {
	Scope             string
	NextRunAt         time.Time
	LastRunAt         *time.Time
	LastStatus        string
	LastError         string
	LastMetrics       int
	LastAPICalls      int
	LastDurationMs    float64
	ConsecutiveErrors int
}

// Interval is how often each scope of the connection is polled.
func (in Input) Interval() time.Duration {
	return time.Duration(in.PollIntervalSeconds) * time.Second
}

// Polls reports whether the connection is collected by the scheduler (a push connection is not).
func (in Input) Polls() bool { return in.IngestMode == IngestPoll }

// Run is one recorded poll of one scope (the history endpoint).
type Run struct {
	ID         int64
	Scope      string
	StartedAt  time.Time
	DurationMs float64
	Status     string
	Metrics    int
	APICalls   int
	Throttled  int
	Error      string
	Services   []ServiceRun
}

// ServiceRun is the per-service outcome inside one run, so a failure names the service it belongs to.
type ServiceRun struct {
	Service string `json:"service"`
	Metrics int    `json:"metrics"`
	Error   string `json:"error"`
}

// Validate checks and normalizes an input.
func (in *Input) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > MaxNameRunes || !utf8.ValidString(in.Name) {
		return invalid("name", "must be 1-200 characters")
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if _, ok := credentialFields[in.Provider]; !ok {
		return invalid("provider", "must be one of "+strings.Join(Providers(), ", "))
	}
	if in.IngestMode == "" {
		in.IngestMode = IngestPoll
	}
	if in.IngestMode != IngestPoll && in.IngestMode != IngestPush {
		return invalid("ingest_mode", "must be poll or push")
	}
	if err := in.validateScopes(); err != nil {
		return err
	}
	if err := in.validateServices(); err != nil {
		return err
	}
	if in.PollIntervalSeconds == 0 {
		in.PollIntervalSeconds = DefaultInterval
	}
	if in.PollIntervalSeconds < MinIntervalSecs || in.PollIntervalSeconds > MaxIntervalSecs {
		return invalid("poll_interval_seconds", "must be between 60 and 86400 seconds")
	}
	if in.MaxMetricsPerPoll == 0 {
		in.MaxMetricsPerPoll = DefaultMetricsPerPoll
	}
	if in.MaxMetricsPerPoll < MinMetricsPerPoll || in.MaxMetricsPerPoll > MaxMetricsPerPoll {
		return invalid("max_metrics_per_poll", "must be between 100 and 200000")
	}
	if in.MaxAPICallsPerPoll == 0 {
		in.MaxAPICallsPerPoll = DefaultAPICallsPerPoll
	}
	if in.MaxAPICallsPerPoll < MinAPICallsPerPoll || in.MaxAPICallsPerPoll > MaxAPICallsPerPoll {
		return invalid("max_api_calls_per_poll", "must be between 1 and 5000")
	}
	if in.Credentials != nil {
		return in.Credentials.Validate(in.Provider)
	}
	return nil
}

// scopeLabel names what a provider's scopes are, for error messages and the UI.
func scopeLabel(provider string) string {
	switch provider {
	case ProviderAWS:
		return "region"
	case ProviderAzure:
		return "subscription"
	case ProviderGCP:
		return "project"
	}
	return "scope"
}

// ScopeLabel is the provider's word for one scope ("region", "subscription", "project").
func ScopeLabel(provider string) string { return scopeLabel(provider) }

func (in *Input) validateScopes() error {
	label := scopeLabel(in.Provider)
	if len(in.Scopes) == 0 {
		return invalid("scopes", "at least one "+label+" is required")
	}
	if len(in.Scopes) > MaxScopes {
		return invalid("scopes", fmt.Sprintf("at most %d %ss", MaxScopes, label))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in.Scopes))
	for i, s := range in.Scopes {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > MaxScopeLen {
			return invalid(fmt.Sprintf("scopes[%d]", i), "must be 1-200 characters")
		}
		// A scope becomes part of a provider URL and of the cloud.region / cloud.account.id attribute, so it
		// is restricted to what those identifiers may contain.
		if !scopeToken(s) {
			return invalid(fmt.Sprintf("scopes[%d]", i), "must contain only letters, digits, '-', '_' and '.'")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	in.Scopes = out
	return nil
}

func scopeToken(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func (in *Input) validateServices() error {
	if len(in.Services) == 0 {
		return invalid("services", "at least one service is required ("+strings.Join(serviceIDs(in.Provider), ", ")+")")
	}
	if len(in.Services) > MaxServices {
		return invalid("services", fmt.Sprintf("at most %d services", MaxServices))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in.Services))
	for i, id := range in.Services {
		id = strings.TrimSpace(id)
		if _, ok := ServiceByID(in.Provider, id); !ok {
			return invalid(fmt.Sprintf("services[%d]", i),
				"unknown "+in.Provider+" service "+strconvQuote(id)+" ("+strings.Join(serviceIDs(in.Provider), ", ")+")")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	// Stored in catalog order, so the list reads the same however it was submitted.
	sort.SliceStable(out, func(i, j int) bool { return catalogIndex(in.Provider, out[i]) < catalogIndex(in.Provider, out[j]) })
	in.Services = out
	return nil
}

func catalogIndex(provider, id string) int {
	for i, s := range Services(provider) {
		if s.ID == id {
			return i
		}
	}
	return len(catalog)
}

// strconvQuote avoids importing strconv for one call site.
func strconvQuote(s string) string { return `"` + s + `"` }

// truncate bounds a stored message.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- stores ----

// Store persists connection definitions per organization (PostgreSQL: PGStore).
type Store interface {
	// List returns the connections of the organization ordered by name, each with its schedule rows.
	List(ctx context.Context, orgID string) ([]Connection, error)
	// Get returns one connection (ErrNotFound when it belongs to another organization).
	Get(ctx context.Context, orgID, id string) (*Connection, error)
	// Credentials returns the stored ciphertext of a connection (ErrNotFound, ErrNoCredentials).
	CredentialsEnc(ctx context.Context, orgID, id string) (string, error)
	// Create stores a new connection under the given id (ErrLimit at MaxPerOrg) and writes the audit event
	// cloud_connection.create. The caller generates the id because it is part of the credential AAD, so the
	// ciphertext is bound to the row before the row exists. credentialsEnc is "" when none were given.
	Create(ctx context.Context, orgID, id string, in Input, credentialsEnc, keyID string, actor Actor) (*Connection, error)
	// Update replaces the writable fields and writes the audit event cloud_connection.update. A nil
	// credentialsEnc keeps the stored credentials.
	Update(ctx context.Context, orgID, id string, in Input, credentialsEnc *string, keyID string, actor Actor) (*Connection, error)
	// Delete removes a connection and writes the audit event cloud_connection.delete.
	Delete(ctx context.Context, orgID, id string, actor Actor) error
	// Runs returns the newest recorded polls of a connection, newest first. scope "" means every scope.
	Runs(ctx context.Context, orgID, id, scope string, limit int) ([]Run, error)
}

// Due is one claimed poll: the definition, the tenant its metrics belong to and the scope to collect.
type Due struct {
	Connection Connection
	TenantID   string
	Scope      string
	// CredentialsEnc is the stored ciphertext; the collector decrypts it per run and never keeps it.
	CredentialsEnc string
}

// ScheduleStore is the scheduler's half of the store (scheduler.go).
type ScheduleStore interface {
	// Claim moves the next_run_at of at most limit due schedule rows forward by their interval and returns
	// them. Claiming and advancing happen in one statement, so two schedulers never poll the same scope twice.
	Claim(ctx context.Context, now time.Time, limit int) ([]Due, error)
	// Record stores the outcome of a poll on its schedule row and appends it to the run history, pruning the
	// history to MaxRunsPerConnection.
	Record(ctx context.Context, connectionID string, r Run, backoff time.Duration) error
}
