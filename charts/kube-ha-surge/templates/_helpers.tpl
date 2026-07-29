{{/*
Standard Helm helpers.
*/}}

{{- define "kube-ha-surge.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kube-ha-surge.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "kube-ha-surge.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kube-ha-surge.labels" -}}
helm.sh/chart: {{ include "kube-ha-surge.chart" . }}
{{ include "kube-ha-surge.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: controller
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "kube-ha-surge.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-ha-surge.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "kube-ha-surge.serviceAccountName" -}}
{{ include "kube-ha-surge.fullname" . }}
{{- end -}}

{{- define "kube-ha-surge.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Derived, contract-fixed resource names. All hang off the fullname base so that
a release named `kube-ha-surge` produces the canonical names the Go code and
docs reference (webhook Service `kube-ha-surge-webhook`, cert Secret
`kube-ha-surge-webhook-tls`, ClusterRole `kube-ha-surge-manager`, ...).
*/}}

{{- define "kube-ha-surge.webhookServiceName" -}}
{{- printf "%s-webhook" (include "kube-ha-surge.fullname" .) -}}
{{- end -}}

{{- define "kube-ha-surge.webhookCertSecretName" -}}
{{- printf "%s-webhook-tls" (include "kube-ha-surge.fullname" .) -}}
{{- end -}}

{{- define "kube-ha-surge.webhookCertificateName" -}}
{{- printf "%s-webhook" (include "kube-ha-surge.fullname" .) -}}
{{- end -}}

{{- define "kube-ha-surge.managerRoleName" -}}
{{- printf "%s-manager" (include "kube-ha-surge.fullname" .) -}}
{{- end -}}

{{/*
Ensure a self-signed webhook serving cert exists when certSource=helm, and
cache it on .Values so every template (the Secret AND the caBundle in the
ValidatingWebhookConfiguration) reads the SAME material within one render pass.

genCA/genSignedCert are non-deterministic, so this MUST be generated exactly
once. The cache-on-.Values pattern makes the result order-independent across
templates. On upgrade the existing Secret is reused via `lookup` so the cert
does not rotate every reconcile.

This helper renders nothing; callers `include` it for the side effect, then
read `.Values._webhookCerts` (a dict with base64 crt/key/ca).
*/}}
{{- define "kube-ha-surge.ensureWebhookCerts" -}}
{{- if not .Values._webhookCerts -}}
  {{- $svc := include "kube-ha-surge.webhookServiceName" . -}}
  {{- $ns := .Release.Namespace -}}
  {{- $cn := printf "%s.%s.svc" $svc $ns -}}
  {{- $altNames := list $cn (printf "%s.%s.svc.cluster.local" $svc $ns) -}}
  {{- $secretName := include "kube-ha-surge.webhookCertSecretName" . -}}
  {{- $existing := lookup "v1" "Secret" $ns $secretName -}}
  {{- if and $existing $existing.data (index $existing.data "tls.crt") (index $existing.data "ca.crt") -}}
    {{- $_ := set .Values "_webhookCerts" (dict "crt" (index $existing.data "tls.crt") "key" (index $existing.data "tls.key") "ca" (index $existing.data "ca.crt")) -}}
  {{- else -}}
    {{- $ca := genCA "kube-ha-surge-webhook-ca" 3650 -}}
    {{- $cert := genSignedCert $cn nil $altNames 3650 $ca -}}
    {{- $_ := set .Values "_webhookCerts" (dict "crt" (b64enc $cert.Cert) "key" (b64enc $cert.Key) "ca" (b64enc $ca.Cert)) -}}
  {{- end -}}
{{- end -}}
{{- end -}}
