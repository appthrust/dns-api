{{- define "dns-api.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dns-api.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "dns-api.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "dns-api.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dns-api.labels" -}}
helm.sh/chart: {{ include "dns-api.chart" . }}
app.kubernetes.io/name: {{ include "dns-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "dns-api.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dns-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller-manager
{{- end -}}

{{- define "dns-api.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (printf "%s-controller-manager" (include "dns-api.fullname" .)) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "dns-api.webhookServiceName" -}}
{{- printf "%s-webhook-service" (include "dns-api.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dns-api.webhookSecretName" -}}
{{- printf "%s-webhook-server-cert" (include "dns-api.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dns-api.webhookCertificateName" -}}
{{- printf "%s-webhook-serving-cert" (include "dns-api.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dns-api.selfSignedIssuerName" -}}
{{- printf "%s-selfsigned-issuer" (include "dns-api.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

