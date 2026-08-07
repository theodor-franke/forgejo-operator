{{/*
This chart assumes one install per cluster: the CRDs, ClusterRole(s), and
ValidatingWebhookConfiguration it manages are all cluster-scoped singletons,
so there's no nameOverride/fullnameOverride machinery here — every resource
name is just derived from the release name directly (not
"<release>-forgejo-operator": longer suffixes like
"-controller-manager-metrics-service" already push that over the 63-char
Kubernetes name limit with the Makefile's own default release name
"forgejo-operator"; measure before adding a longer prefix back). Even so,
a sufficiently long --name at install time can still exceed 63 chars for
the longest suffixes — this chart doesn't do the old plugin's dynamic
per-suffix truncation math, as a deliberate simplicity tradeoff.
*/}}
{{- define "forgejo-operator.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "forgejo-operator.labels" -}}
app.kubernetes.io/name: forgejo-operator
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end -}}
