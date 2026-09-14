{{/* ---------------------------------------------------------------------------
Names and labels
--------------------------------------------------------------------------- */}}

{{- define "openlog.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fullname is kept short (<= 40 chars) because components, operator resources and
     operator-generated pods append suffixes to it. */}}
{{- define "openlog.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 40 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 40 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 40 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "openlog.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "openlog.labels" -}}
helm.sh/chart: {{ include "openlog.chart" . }}
app.kubernetes.io/name: {{ include "openlog.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: openlog
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* ctx: dict "root" $ "component" "<name>" */}}
{{- define "openlog.componentName" -}}
{{- printf "%s-%s" (include "openlog.fullname" .root) .component | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "openlog.selectorLabels" -}}
app.kubernetes.io/name: {{ include "openlog.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "openlog.componentLabels" -}}
{{ include "openlog.labels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "openlog.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag | toString) -}}
{{- end -}}

{{- define "openlog.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "openlog.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* ---------------------------------------------------------------------------
Secrets
--------------------------------------------------------------------------- */}}

{{- define "openlog.secretName" -}}
{{- if .Values.auth.existingSecret -}}
{{- .Values.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-credentials" (include "openlog.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "openlog.secret.clickhousePasswordKey" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.auth.clickhousePasswordKey }}{{- else -}}clickhouse-password{{- end -}}
{{- end -}}

{{- define "openlog.secret.licenseKeysKey" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.auth.licenseKeysKey }}{{- else -}}license-keys{{- end -}}
{{- end -}}

{{- define "openlog.secret.postgresDsnKey" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.auth.postgresDsnKey }}{{- else -}}postgres-dsn{{- end -}}
{{- end -}}

{{- define "openlog.secret.bootstrapPasswordKey" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.bootstrap.ownerPasswordKey }}{{- else -}}bootstrap-owner-password{{- end -}}
{{- end -}}

{{- define "openlog.secret.bootstrapLicenseKeyKey" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.bootstrap.licenseKeyKey }}{{- else -}}bootstrap-license-key{{- end -}}
{{- end -}}

{{/* ---------------------------------------------------------------------------
PostgreSQL (auth.mode=postgres)
--------------------------------------------------------------------------- */}}

{{- define "openlog.postgres.enabled" -}}
{{- if eq .Values.auth.mode "postgres" -}}true{{- end -}}
{{- end -}}

{{- define "openlog.postgres.operator" -}}
{{- if and (eq .Values.auth.mode "postgres") (eq .Values.postgres.mode "operator") -}}true{{- end -}}
{{- end -}}

{{/* CloudNativePG Cluster name; CNPG creates the Secret <name>-app and the Service <name>-rw. */}}
{{- define "openlog.postgres.name" -}}
{{- default (printf "%s-pg" (include "openlog.fullname" .)) .Values.postgres.operator.name | trunc 40 | trimSuffix "-" -}}
{{- end -}}

{{/* Connection env for ingest, api, migrate and bootstrap. ctx: root. */}}
{{- define "openlog.postgresEnv" -}}
- name: OPENLOG_AUTH_MODE
  value: "postgres"
{{- if include "openlog.postgres.operator" . }}
# CloudNativePG generates the app user's password and a ready-to-use URI (TLS enabled).
- name: OPENLOG_POSTGRES_DSN
  valueFrom:
    secretKeyRef:
      name: {{ printf "%s-app" (include "openlog.postgres.name" .) }}
      key: uri
{{- else }}
- name: OPENLOG_POSTGRES_DSN
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" . }}
      key: {{ include "openlog.secret.postgresDsnKey" . }}
{{- with .Values.postgres.external.passwordSecret.name }}
- name: OPENLOG_POSTGRES_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ . }}
      key: {{ $.Values.postgres.external.passwordSecret.key }}
{{- end }}
{{- end }}
- name: OPENLOG_POSTGRES_MAX_CONNS
  value: {{ include "openlog.envValue" .Values.postgres.maxConns }}
{{- end -}}

{{/* ---------------------------------------------------------------------------
Dependencies (external vs operators)
--------------------------------------------------------------------------- */}}

{{- define "openlog.operators" -}}
{{- if eq .Values.dependencies.mode "operators" -}}true{{- end -}}
{{- end -}}

{{- define "openlog.kafka.name" -}}
{{- default (include "openlog.fullname" .) .Values.kafka.strimzi.name | trunc 40 | trimSuffix "-" -}}
{{- end -}}

{{- define "openlog.clickhouse.name" -}}
{{- default (include "openlog.fullname" .) .Values.clickhouse.altinity.name | trunc 30 | trimSuffix "-" -}}
{{- end -}}

{{- define "openlog.keeper.name" -}}
{{- default (include "openlog.fullname" .) .Values.clickhouse.altinity.keeper.name | trunc 30 | trimSuffix "-" -}}
{{- end -}}

{{- define "openlog.kafka.brokers" -}}
{{- if include "openlog.operators" . -}}
{{- printf "%s-kafka-bootstrap.%s.svc:%d" (include "openlog.kafka.name" .) .Release.Namespace (int .Values.kafka.strimzi.listenerPort) -}}
{{- else -}}
{{- join "," .Values.kafka.external.brokers -}}
{{- end -}}
{{- end -}}

{{- define "openlog.clickhouse.addr" -}}
{{- if include "openlog.operators" . -}}
{{- $host := default (printf "clickhouse-%s.%s.svc" (include "openlog.clickhouse.name" .) .Release.Namespace) .Values.clickhouse.altinity.host -}}
{{- printf "%s:%d" $host (ternary (int .Values.clickhouse.tls.securePort) 9000 (not (not .Values.clickhouse.tls.enabled))) -}}
{{- else -}}
{{- join "," .Values.clickhouse.external.addrs -}}
{{- end -}}
{{- end -}}

{{- define "openlog.clickhouse.user" -}}
{{- if .Values.clickhouse.user -}}
{{- .Values.clickhouse.user -}}
{{- else if include "openlog.operators" . -}}
openlog
{{- else -}}
default
{{- end -}}
{{- end -}}

{{/* Read-only user of api/alert queries (D-047); empty when not used. ctx: root */}}
{{- define "openlog.clickhouse.readUser" -}}
{{- $r := .Values.clickhouse.readUser -}}
{{- if $r.enabled -}}
{{- if .Values.auth.existingSecret -}}
{{- if .Values.auth.clickhouseReadPasswordKey -}}{{ $r.name }}{{- end -}}
{{- else if or (include "openlog.operators" .) $r.password -}}
{{- $r.name -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "openlog.secret.clickhouseReadPasswordKey" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.auth.clickhouseReadPasswordKey }}{{- else -}}clickhouse-read-password{{- end -}}
{{- end -}}

{{/* ClickHouse server TLS configured by the chart (operators mode + clickhouse.tls.server.secretName). */}}
{{- define "openlog.clickhouse.serverTLS" -}}
{{- if and (include "openlog.operators" .) .Values.clickhouse.tls.server.secretName -}}true{{- end -}}
{{- end -}}

{{- define "openlog.keeper.host" -}}
{{- default (printf "keeper-%s.%s.svc" (include "openlog.keeper.name" .) .Release.Namespace) .Values.clickhouse.altinity.keeper.host -}}
{{- end -}}

{{/* Topic creation by openlog-migrate is skipped when Strimzi owns topics or when topic
     management is disabled altogether. */}}
{{- define "openlog.migrate.skipKafka" -}}
{{- if or (include "openlog.operators" .) (not .Values.kafka.topics.create) -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{/* In operators mode ClickHouse/Kafka (and with postgres.mode=operator the CloudNativePG
     Cluster) are created by this release, so migrations cannot run pre-install (nothing
     exists yet); they run post-install instead. */}}
{{- define "openlog.migrate.hookEvents" -}}
{{- if .Values.migrate.hookEvents -}}
{{- .Values.migrate.hookEvents -}}
{{- else if or (include "openlog.operators" .) (include "openlog.postgres.operator" .) -}}
post-install,pre-upgrade
{{- else -}}
pre-install,pre-upgrade
{{- end -}}
{{- end -}}

{{/* ---------------------------------------------------------------------------
TLS / SASL (docs/contracts/config.md "TLS and SASL"). Secrets are mounted read-only under
/etc/openlog/tls/<volume>/ with fixed file names (ca.crt, tls.crt, tls.key) in every openlog pod,
because every binary validates all configured files at start-up. ctx: root.
--------------------------------------------------------------------------- */}}

{{- define "openlog.kafka.strimziTLS" -}}
{{- if and (include "openlog.operators" .) .Values.kafka.strimzi.listener.tls -}}true{{- end -}}
{{- end -}}

{{- define "openlog.kafka.strimziAuth" -}}
{{- if include "openlog.operators" . -}}{{- .Values.kafka.strimzi.listener.authentication -}}{{- end -}}
{{- end -}}

{{/* KafkaUser rendered for listener authentication (operators mode). */}}
{{- define "openlog.kafka.user" -}}
{{- default (printf "%s-openlog" (include "openlog.kafka.name" .)) .Values.kafka.strimzi.listener.user | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* JSON {"mounts": [{volume, secret, items: [{key, path}]}]} of the TLS secrets to mount. */}}
{{- define "openlog.tls.mounts" -}}
{{- $m := list -}}
{{- $k := .Values.kafka.tls -}}
{{- if or $k.enabled (include "openlog.kafka.strimziTLS" .) -}}
{{- $ca := $k.caSecret.name -}}
{{- if and (not $ca) (include "openlog.kafka.strimziTLS" .) -}}
{{- $ca = printf "%s-cluster-ca-cert" (include "openlog.kafka.name" .) -}}
{{- end -}}
{{- if $ca -}}
{{- $m = append $m (dict "volume" "kafka-ca" "secret" $ca "items" (list (dict "key" $k.caSecret.key "path" "ca.crt"))) -}}
{{- end -}}
{{- if eq (include "openlog.kafka.strimziAuth" .) "tls" -}}
{{- $m = append $m (dict "volume" "kafka-client" "secret" (include "openlog.kafka.user" .) "items" (list (dict "key" "user.crt" "path" "tls.crt") (dict "key" "user.key" "path" "tls.key"))) -}}
{{- else if $k.clientSecret.name -}}
{{- $m = append $m (dict "volume" "kafka-client" "secret" $k.clientSecret.name "items" (list (dict "key" $k.clientSecret.certKey "path" "tls.crt") (dict "key" $k.clientSecret.keyKey "path" "tls.key"))) -}}
{{- end -}}
{{- end -}}
{{- $c := .Values.clickhouse.tls -}}
{{- if $c.enabled -}}
{{- $caName := $c.caSecret.name -}}
{{- $caKey := $c.caSecret.key -}}
{{- if and (not $caName) (include "openlog.clickhouse.serverTLS" .) -}}
{{- $caName = $c.server.secretName -}}
{{- $caKey = $c.server.caKey -}}
{{- end -}}
{{- if $caName -}}
{{- $m = append $m (dict "volume" "clickhouse-ca" "secret" $caName "items" (list (dict "key" $caKey "path" "ca.crt"))) -}}
{{- end -}}
{{- with $c.clientSecret.name -}}
{{- $m = append $m (dict "volume" "clickhouse-client" "secret" . "items" (list (dict "key" $c.clientSecret.certKey "path" "tls.crt") (dict "key" $c.clientSecret.keyKey "path" "tls.key"))) -}}
{{- end -}}
{{- end -}}
{{- if include "openlog.postgres.enabled" . -}}
{{- $p := .Values.postgres.tls -}}
{{- with $p.caSecret.name -}}
{{- $m = append $m (dict "volume" "postgres-ca" "secret" . "items" (list (dict "key" $p.caSecret.key "path" "ca.crt"))) -}}
{{- end -}}
{{- with $p.clientSecret.name -}}
{{- $m = append $m (dict "volume" "postgres-client" "secret" . "items" (list (dict "key" $p.clientSecret.certKey "path" "tls.crt") (dict "key" $p.clientSecret.keyKey "path" "tls.key"))) -}}
{{- end -}}
{{- end -}}
{{- dict "mounts" $m | toJson -}}
{{- end -}}

{{- define "openlog.tls.hasMount" -}}
{{- range (include "openlog.tls.mounts" .root | fromJson).mounts -}}
{{- if eq .volume $.volume -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{/* volumeMounts entries (possibly none). ctx: root */}}
{{- define "openlog.tls.volumeMounts" -}}
{{- range (include "openlog.tls.mounts" . | fromJson).mounts }}
- name: tls-{{ .volume }}
  mountPath: /etc/openlog/tls/{{ .volume }}
  readOnly: true
{{- end }}
{{- end -}}

{{/* volumes entries (possibly none). ctx: root */}}
{{- define "openlog.tls.volumes" -}}
{{- range (include "openlog.tls.mounts" . | fromJson).mounts }}
- name: tls-{{ .volume }}
  secret:
    secretName: {{ .secret }}
    defaultMode: 0440
    items:
      {{- toYaml .items | nindent 6 }}
{{- end }}
{{- end -}}

{{/* OPENLOG_{KAFKA,CLICKHOUSE,POSTGRES}_TLS_* and OPENLOG_KAFKA_SASL_* env. ctx: root */}}
{{- define "openlog.tls.env" -}}
{{- $root := . -}}
{{- $k := .Values.kafka.tls -}}
{{- if or $k.enabled (include "openlog.kafka.strimziTLS" .) }}
- name: OPENLOG_KAFKA_TLS_ENABLED
  value: "true"
{{- if include "openlog.tls.hasMount" (dict "root" $root "volume" "kafka-ca") }}
- name: OPENLOG_KAFKA_TLS_CA_FILE
  value: /etc/openlog/tls/kafka-ca/ca.crt
{{- end }}
{{- if include "openlog.tls.hasMount" (dict "root" $root "volume" "kafka-client") }}
- name: OPENLOG_KAFKA_TLS_CERT_FILE
  value: /etc/openlog/tls/kafka-client/tls.crt
- name: OPENLOG_KAFKA_TLS_KEY_FILE
  value: /etc/openlog/tls/kafka-client/tls.key
{{- end }}
{{- with $k.serverName }}
- name: OPENLOG_KAFKA_TLS_SERVER_NAME
  value: {{ . | quote }}
{{- end }}
{{- if $k.insecureSkipVerify }}
- name: OPENLOG_KAFKA_TLS_INSECURE_SKIP_VERIFY
  value: "true"
{{- end }}
{{- end }}
{{- if eq (include "openlog.kafka.strimziAuth" .) "scram-sha-512" }}
# Strimzi KafkaUser {{ include "openlog.kafka.user" . }} (the User Operator writes its password Secret).
- name: OPENLOG_KAFKA_SASL_MECHANISM
  value: SCRAM-SHA-512
- name: OPENLOG_KAFKA_SASL_USERNAME
  value: {{ include "openlog.kafka.user" . | quote }}
- name: OPENLOG_KAFKA_SASL_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.kafka.user" . }}
      key: password
{{- else if .Values.kafka.sasl.mechanism }}
{{- $s := .Values.kafka.sasl }}
- name: OPENLOG_KAFKA_SASL_MECHANISM
  value: {{ $s.mechanism | quote }}
- name: OPENLOG_KAFKA_SASL_USERNAME
  value: {{ $s.username | quote }}
- name: OPENLOG_KAFKA_SASL_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ $s.passwordSecret.name }}
      key: {{ $s.passwordSecret.key }}
{{- end }}
{{- $c := .Values.clickhouse.tls -}}
{{- if $c.enabled }}
- name: OPENLOG_CLICKHOUSE_TLS_ENABLED
  value: "true"
{{- if include "openlog.tls.hasMount" (dict "root" $root "volume" "clickhouse-ca") }}
- name: OPENLOG_CLICKHOUSE_TLS_CA_FILE
  value: /etc/openlog/tls/clickhouse-ca/ca.crt
{{- end }}
{{- if $c.clientSecret.name }}
- name: OPENLOG_CLICKHOUSE_TLS_CERT_FILE
  value: /etc/openlog/tls/clickhouse-client/tls.crt
- name: OPENLOG_CLICKHOUSE_TLS_KEY_FILE
  value: /etc/openlog/tls/clickhouse-client/tls.key
{{- end }}
{{- with $c.serverName }}
- name: OPENLOG_CLICKHOUSE_TLS_SERVER_NAME
  value: {{ . | quote }}
{{- end }}
{{- if $c.insecureSkipVerify }}
- name: OPENLOG_CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY
  value: "true"
{{- end }}
{{- end }}
{{- if include "openlog.postgres.enabled" . }}
{{- $p := .Values.postgres.tls }}
{{- if $p.caSecret.name }}
- name: OPENLOG_POSTGRES_TLS_CA_FILE
  value: /etc/openlog/tls/postgres-ca/ca.crt
{{- end }}
{{- if $p.clientSecret.name }}
- name: OPENLOG_POSTGRES_TLS_CERT_FILE
  value: /etc/openlog/tls/postgres-client/tls.crt
- name: OPENLOG_POSTGRES_TLS_KEY_FILE
  value: /etc/openlog/tls/postgres-client/tls.key
{{- end }}
{{- end }}
{{- end -}}

{{/* ---------------------------------------------------------------------------
Environment
ctx: dict "root" $ "component" "<name>" "values" <component values>
--------------------------------------------------------------------------- */}}

{{/* Render a scalar env value; integers coming from YAML are float64 in Helm and
     would otherwise print as 1.048576e+07. */}}
{{- define "openlog.envValue" -}}
{{- if and (kindIs "float64" .) (eq (float64 (int64 .)) .) -}}
{{- int64 . | toString | quote -}}
{{- else -}}
{{- toString . | quote -}}
{{- end -}}
{{- end -}}

{{- define "openlog.env" -}}
{{- $root := .root -}}
{{- $c := .component -}}
- name: OPENLOG_LOG_LEVEL
  value: {{ default $root.Values.logLevel .values.logLevel | quote }}
- name: OPENLOG_ADMIN_ADDR
  value: ":9464"
# Go soft memory limit = the container memory limit, so the GC works harder before the
# cgroup OOM killer steps in (a pod without a memory limit gets node allocatable memory).
- name: GOMEMLIMIT
  valueFrom:
    resourceFieldRef:
      resource: limits.memory
- name: OPENLOG_KAFKA_BROKERS
  value: {{ include "openlog.kafka.brokers" $root | quote }}
- name: OPENLOG_KAFKA_TOPIC_PREFIX
  value: {{ $root.Values.kafka.topicPrefix | quote }}
- name: OPENLOG_CLICKHOUSE_ADDR
  value: {{ include "openlog.clickhouse.addr" $root | quote }}
- name: OPENLOG_CLICKHOUSE_DATABASE
  # Fixed by contract (D-015): the schema hard-codes the `openlog` database.
  value: "openlog"
- name: OPENLOG_CLICKHOUSE_USER
  value: {{ include "openlog.clickhouse.user" $root | quote }}
- name: OPENLOG_CLICKHOUSE_CLUSTER
  value: {{ $root.Values.clickhouse.cluster | quote }}
{{- include "openlog.tls.env" $root }}
{{- if has $c (list "ingest" "processor" "api" "sampler") }}
{{- /* Tail sampling (D-075): the flag must match on ingest, processor and sampler. */}}
{{- $ts := $root.Values.tailSampling }}
- name: OPENLOG_TAILSAMPLING_ENABLED
  value: {{ $ts.enabled | toString | quote }}
{{- if eq $c "sampler" }}
- name: OPENLOG_TAILSAMPLING_DECISION_WAIT
  value: {{ $ts.decisionWait | quote }}
- name: OPENLOG_TAILSAMPLING_MAX_TRACES
  value: {{ include "openlog.envValue" $ts.maxTraces }}
- name: OPENLOG_TAILSAMPLING_MAX_SPANS_PER_TRACE
  value: {{ include "openlog.envValue" $ts.maxSpansPerTrace }}
- name: OPENLOG_TAILSAMPLING_MAX_BUFFERED_BYTES
  value: {{ include "openlog.envValue" $ts.maxBufferedBytes }}
- name: OPENLOG_TAILSAMPLING_DECISION_CACHE_TTL
  value: {{ $ts.decisionCacheTTL | quote }}
- name: OPENLOG_TAILSAMPLING_DECISION_CACHE_SIZE
  value: {{ include "openlog.envValue" $ts.decisionCacheSize }}
- name: OPENLOG_TAILSAMPLING_POLICY_REFRESH
  value: {{ $ts.policyRefresh | quote }}
- name: OPENLOG_TAILSAMPLING_DEFAULT_POLICY
  value: {{ $ts.defaultPolicy | quote }}
- name: OPENLOG_TAILSAMPLING_GROUP
  value: {{ $ts.group | default "openlog-sampler" | quote }}
- name: OPENLOG_TAILSAMPLING_PRODUCE_TIMEOUT
  value: {{ $ts.produceTimeout | default "10s" | quote }}
{{- end }}
{{- end }}
{{- if has $c (list "processor" "api" "migrate" "alert") }}
{{- /* alert: evaluation queries and alert_evaluations inserts */}}
- name: OPENLOG_CLICKHOUSE_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.clickhousePasswordKey" $root }}
      optional: {{ not (include "openlog.operators" $root) }}
{{- end }}
{{- if has $c (list "api" "alert") }}
{{- with include "openlog.clickhouse.readUser" $root }}
# Tenant queries as the read-only ClickHouse user (D-047).
- name: OPENLOG_CLICKHOUSE_READ_USER
  value: {{ . | quote }}
- name: OPENLOG_CLICKHOUSE_READ_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.clickhouseReadPasswordKey" $root }}
{{- end }}
{{- with $root.Values.clickhouse.queryLimits }}
- name: OPENLOG_QUERY_MAX_MEMORY_USAGE
  value: {{ include "openlog.envValue" .maxMemoryUsage }}
- name: OPENLOG_QUERY_MAX_ROWS_TO_READ
  value: {{ include "openlog.envValue" .maxRowsToRead }}
- name: OPENLOG_QUERY_MAX_BYTES_TO_READ
  value: {{ include "openlog.envValue" .maxBytesToRead }}
{{- with .tenantLimits }}
- name: OPENLOG_QUERY_TENANT_LIMITS
  value: {{ . | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- if not (include "openlog.postgres.enabled" $root) }}
{{- if has $c (list "ingest" "api" "migrate") }}
# auth.mode=static: development/tests only (no users, no UI sign-in).
- name: OPENLOG_AUTH_MODE
  value: "static"
{{- end }}
{{- if has $c (list "ingest" "api") }}
- name: OPENLOG_LICENSE_KEYS
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.licenseKeysKey" $root }}
      optional: true
{{- end }}
{{- else if has $c (list "ingest" "processor" "api" "migrate" "updater" "alert" "sampler") }}
{{- /* processor: component_heartbeats for contract migrations; updater: status + audit log */}}
{{ include "openlog.postgresEnv" $root }}
{{- end }}
{{- if and (eq $c "ingest") (include "openlog.postgres.enabled" $root) }}
- name: OPENLOG_AUTH_CACHE_TTL
  value: {{ $root.Values.auth.cache.ttl | quote }}
- name: OPENLOG_AUTH_NEGATIVE_CACHE_TTL
  value: {{ $root.Values.auth.cache.negativeTTL | quote }}
- name: OPENLOG_AUTH_CACHE_MAX_STALE
  value: {{ $root.Values.auth.cache.maxStale | quote }}
{{- end }}
{{- if and (eq $c "api") (include "openlog.postgres.enabled" $root) }}
{{- $a := $root.Values.auth }}
- name: OPENLOG_SESSION_TTL
  value: {{ $a.session.ttl | quote }}
- name: OPENLOG_SESSION_IDLE_TIMEOUT
  value: {{ $a.session.idleTimeout | quote }}
- name: OPENLOG_COOKIE_SECURE
  value: {{ $a.session.cookieSecure | toString | quote }}
- name: OPENLOG_COOKIE_DOMAIN
  value: {{ $a.session.cookieDomain | quote }}
- name: OPENLOG_SIGNUP_ENABLED
  value: {{ $a.signupEnabled | toString | quote }}
- name: OPENLOG_LOGIN_MAX_FAILURES
  value: {{ include "openlog.envValue" $a.login.maxFailures }}
- name: OPENLOG_LOGIN_WINDOW
  value: {{ $a.login.window | quote }}
- name: OPENLOG_INVITATION_TTL
  value: {{ $a.invitationTTL | quote }}
- name: OPENLOG_API_TRUSTED_PROXIES
  value: {{ join "," $a.trustedProxies | quote }}
{{- /* Single sign-on secret key and sign-up CAPTCHA secret (chart Secret or auth.existingSecret; absent keys = unset). */}}
{{- range $env := list (list "OPENLOG_SSO_SECRET_KEY" $a.ssoSecretKeyKey "sso-secret-key") (list "OPENLOG_SSO_SECRET_KEY_PREVIOUS" $a.ssoSecretKeyPreviousKey "sso-secret-key-previous") (list "OPENLOG_SIGNUP_CAPTCHA_SECRET" $a.captchaSecretKey "signup-captcha-secret") }}
- name: {{ index $env 0 }}
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.keyName" (dict "root" $root "existing" (index $env 1) "chart" (index $env 2)) }}
      optional: true
{{- end }}
{{- end }}
{{- if and (has $c (list "ingest" "api")) (include "openlog.postgres.enabled" $root) }}
{{ include "openlog.keyHashEnv" $root }}
{{- end }}
{{- if and (eq $c "ingest") (include "openlog.postgres.enabled" $root) }}
{{- /* Integration setting passwords in agent sync answers (releases-updates.md §3). */}}
- name: OPENLOG_SECRETS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.alertSecretsKeyKey" $root }}
      optional: true
- name: OPENLOG_SECRETS_KEY_PREVIOUS
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.alertSecretsKeyPreviousKey" $root }}
      optional: true
{{- end }}
{{- if and (has $c (list "api" "alert")) (include "openlog.postgres.enabled" $root) }}
{{- /* Alerting (docs/contracts/alerting.md): channel secret encryption, links, egress policy, SMTP. */}}
{{- $al := $root.Values.alert }}
- name: OPENLOG_SECRETS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.alertSecretsKeyKey" $root }}
      optional: true
- name: OPENLOG_SECRETS_KEY_PREVIOUS
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.alertSecretsKeyPreviousKey" $root }}
      optional: true
- name: OPENLOG_PUBLIC_URL
  value: {{ $al.publicURL | quote }}
- name: OPENLOG_ALERT_BLOCK_PRIVATE_DESTINATIONS
  value: {{ $al.blockPrivateDestinations | toString | quote }}
- name: OPENLOG_SMTP_HOST
  value: {{ $al.smtp.host | quote }}
- name: OPENLOG_SMTP_PORT
  value: {{ include "openlog.envValue" $al.smtp.port }}
- name: OPENLOG_SMTP_USERNAME
  value: {{ $al.smtp.username | quote }}
- name: OPENLOG_SMTP_FROM
  value: {{ $al.smtp.from | quote }}
- name: OPENLOG_SMTP_TLS
  value: {{ $al.smtp.tls | quote }}
- name: OPENLOG_SMTP_INSECURE_SKIP_VERIFY
  value: {{ $al.smtp.insecureSkipVerify | toString | quote }}
{{- with $al.smtp.passwordSecret.name }}
- name: OPENLOG_SMTP_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ . }}
      key: {{ $al.smtp.passwordSecret.key }}
{{- else }}
- name: OPENLOG_SMTP_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.keyName" (dict "root" $root "existing" $al.smtp.passwordKey "chart" "smtp-password") }}
      optional: true
{{- end }}
{{- end }}
{{- if eq $c "migrate" }}
- name: OPENLOG_KAFKA_PARTITIONS
  value: {{ include "openlog.envValue" $root.Values.kafka.topics.partitions }}
- name: OPENLOG_KAFKA_REPLICATION_FACTOR
  value: {{ include "openlog.envValue" $root.Values.kafka.topics.replicationFactor }}
- name: OPENLOG_KAFKA_MIN_INSYNC_REPLICAS
  value: {{ include "openlog.envValue" $root.Values.kafka.topics.minInsyncReplicas }}
- name: OPENLOG_KAFKA_RETENTION_MS
  value: {{ include "openlog.envValue" $root.Values.kafka.topics.retentionMs }}
- name: OPENLOG_KAFKA_MAX_MESSAGE_BYTES
  value: {{ include "openlog.envValue" $root.Values.kafka.topics.maxMessageBytes }}
- name: OPENLOG_MIGRATE_SKIP_KAFKA
  value: {{ include "openlog.migrate.skipKafka" $root | quote }}
{{- end }}
{{- if has $c (list "migrate" "api") }}
{{- /* migrate applies the moves; api pods run `openlog-admin storage status` (docs/operations/tiered-storage.md). */}}
{{- with $root.Values.clickhouse.tieredStorage }}
{{- $tiering := .enabled }}
{{- /* Operators mode: the pre-upgrade migrate hook runs before the ClickHouseInstallation receives the storage policy.
     On the upgrade that enables tiering, migrate would fail ("storage policy not usable") and block the CHI change
     forever (seen on kind, 2026-09-14). Keep it off for migrate while the live CHI lacks the policy file; the next
     `helm upgrade` applies the moves. A fresh install (no CHI yet, post-install hook) and `helm template` keep it on. */}}
{{- if and .enabled (eq $c "migrate") (include "openlog.operators" $root) }}
{{- $chi := lookup "clickhouse.altinity.com/v1" "ClickHouseInstallation" $root.Release.Namespace (include "openlog.clickhouse.name" $root) }}
{{- if and $chi (not (hasKey (dig "spec" "configuration" "files" dict $chi) "config.d/openlog-storage.xml")) }}
{{- $tiering = false }}
{{- end }}
{{- end }}
- name: OPENLOG_STORAGE_TIERING_ENABLED
  value: {{ $tiering | toString | quote }}
- name: OPENLOG_STORAGE_POLICY
  value: {{ .policy | quote }}
{{- range $class, $days := .coldAfterDays }}
- name: OPENLOG_STORAGE_COLD_AFTER_DAYS_{{ upper $class }}
  value: {{ include "openlog.envValue" $days }}
{{- end }}
{{- range $class, $days := .warmAfterDays }}
- name: OPENLOG_STORAGE_WARM_AFTER_DAYS_{{ upper $class }}
  value: {{ include "openlog.envValue" $days }}
{{- end }}
{{- end }}
{{- end }}
{{- /* Component config wins over the top-level config for the same name (explicit sets: sprig merge would let a
     boolean false lose). */}}
{{- $cfg := dict }}
{{- range $k, $v := ($root.Values.config | default dict) }}{{- $_ := set $cfg $k $v }}{{- end }}
{{- range $k, $v := (.values.config | default dict) }}{{- $_ := set $cfg $k $v }}{{- end }}
{{- range $k, $v := $cfg }}
- name: {{ $k }}
  value: {{ include "openlog.envValue" $v }}
{{- end }}
{{- with $root.Values.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- with .values.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* Key name in the credentials Secret: the configured one with auth.existingSecret, else the chart's.
     ctx: dict "root" $ "existing" <key name setting> "chart" <chart key> */}}
{{- define "openlog.secret.keyName" -}}
{{- if .root.Values.auth.existingSecret -}}{{ .existing }}{{- else -}}{{ .chart }}{{- end -}}
{{- end -}}

{{/* OPENLOG_KEY_HASH_SECRET(_PREVIOUS) of ingest, api and the bootstrap Job (D-044: same value everywhere). ctx: root */}}
{{- define "openlog.keyHashEnv" -}}
- name: OPENLOG_KEY_HASH_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" . }}
      key: {{ include "openlog.secret.keyName" (dict "root" . "existing" .Values.auth.keyHashSecretKey "chart" "key-hash-secret") }}
      optional: true
- name: OPENLOG_KEY_HASH_SECRET_PREVIOUS
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" . }}
      key: {{ include "openlog.secret.keyName" (dict "root" . "existing" .Values.auth.keyHashSecretPreviousKey "chart" "key-hash-secret-previous") }}
      optional: true
{{- end -}}

{{/* Secret keys of the alerting encryption keys (chart Secret or auth.existingSecret). ctx: root */}}
{{- define "openlog.secret.alertSecretsKeyKey" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.alert.secretsKeyKey }}{{- else -}}secrets-key{{- end -}}
{{- end -}}

{{- define "openlog.secret.alertSecretsKeyPreviousKey" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.alert.secretsKeyPreviousKey }}{{- else -}}secrets-key-previous{{- end -}}
{{- end -}}

{{/* ---------------------------------------------------------------------------
Pod scheduling (anti-affinity + topology spread)
ctx: dict "root" $ "component" "<name>" "values" <component values>
--------------------------------------------------------------------------- */}}
{{- define "openlog.scheduling" -}}
{{- $root := .root -}}
{{- with .values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .values.priorityClassName }}
priorityClassName: {{ . }}
{{- end }}
{{- if .values.affinity }}
affinity:
  {{- toYaml .values.affinity | nindent 2 }}
{{- else if eq $root.Values.scheduling.podAntiAffinity "soft" }}
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              {{- include "openlog.selectorLabels" . | nindent 14 }}
{{- else if eq $root.Values.scheduling.podAntiAffinity "hard" }}
affinity:
  podAntiAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      - topologyKey: kubernetes.io/hostname
        labelSelector:
          matchLabels:
            {{- include "openlog.selectorLabels" . | nindent 12 }}
{{- end }}
{{- if .values.topologySpreadConstraints }}
topologySpreadConstraints:
  {{- toYaml .values.topologySpreadConstraints | nindent 2 }}
{{- else if $root.Values.scheduling.topologySpread.enabled }}
topologySpreadConstraints:
  - maxSkew: {{ $root.Values.scheduling.topologySpread.maxSkew }}
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: {{ $root.Values.scheduling.topologySpread.whenUnsatisfiable }}
    matchLabelKeys: [pod-template-hash]
    labelSelector:
      matchLabels:
        {{- include "openlog.selectorLabels" . | nindent 8 }}
  {{- if $root.Values.scheduling.topologySpread.zones }}
  - maxSkew: {{ $root.Values.scheduling.topologySpread.maxSkew }}
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: {{ $root.Values.scheduling.topologySpread.whenUnsatisfiable }}
    matchLabelKeys: [pod-template-hash]
    labelSelector:
      matchLabels:
        {{- include "openlog.selectorLabels" . | nindent 8 }}
  {{- end }}
{{- end }}
{{- end -}}

{{- define "openlog.podSecurity" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
securityContext:
  {{- toYaml .Values.podSecurityContext | nindent 2 }}
{{- end -}}
