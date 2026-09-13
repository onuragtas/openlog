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
{{- with $c.caSecret.name -}}
{{- $m = append $m (dict "volume" "clickhouse-ca" "secret" . "items" (list (dict "key" $c.caSecret.key "path" "ca.crt"))) -}}
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
{{- if $c.caSecret.name }}
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
{{- if has $c (list "processor" "api" "migrate") }}
- name: OPENLOG_CLICKHOUSE_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog.secretName" $root }}
      key: {{ include "openlog.secret.clickhousePasswordKey" $root }}
      optional: {{ not (include "openlog.operators" $root) }}
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
{{- else if has $c (list "ingest" "processor" "api" "migrate" "updater" "alert") }}
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
{{- range $k, $v := .values.config }}
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
