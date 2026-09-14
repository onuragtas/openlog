{{- define "openlog-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "openlog-agent.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 50 | trimSuffix "-" -}}
{{- else if contains (include "openlog-agent.name" .) .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "openlog-agent.name" .) | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "openlog-agent.labels" -}}
app.kubernetes.io/name: {{ include "openlog-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "openlog-agent.selector" -}}
app.kubernetes.io/name: {{ include "openlog-agent.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "openlog-agent.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{- define "openlog-agent.secretName" -}}
{{- if .Values.existingSecret.name -}}
{{- .Values.existingSecret.name -}}
{{- else -}}
{{- printf "%s-license" (include "openlog-agent.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/* Environment shared by both workloads. */}}
{{- define "openlog-agent.env" -}}
- name: OPENLOG_LICENSE_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "openlog-agent.secretName" . }}
      key: {{ .Values.existingSecret.key }}
- name: OPENLOG_ENDPOINT
  value: {{ .Values.endpoint | quote }}
- name: OPENLOG_K8S_CLUSTER_NAME
  value: {{ .Values.clusterName | quote }}
- name: OPENLOG_K8S_NODE_NAME
  valueFrom:
    fieldRef:
      fieldPath: spec.nodeName
- name: OPENLOG_K8S_POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: GOMEMLIMIT
  valueFrom:
    resourceFieldRef:
      resource: limits.memory
      divisor: "1"
{{- with .Values.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* Restricted container security context of the cluster collector. */}}
{{- define "openlog-agent.restrictedSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
runAsUser: 65534
runAsGroup: 65534
capabilities:
  drop: ["ALL"]
seccompProfile:
  type: RuntimeDefault
{{- end -}}
