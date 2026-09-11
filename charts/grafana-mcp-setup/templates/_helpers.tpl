{{- define "grafana-mcp-setup.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "grafana-mcp-setup.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "grafana-mcp-setup.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "grafana-mcp-setup.labels" -}}
helm.sh/chart: {{ include "grafana-mcp-setup.chart" . }}
{{ include "grafana-mcp-setup.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "grafana-mcp-setup.selectorLabels" -}}
app.kubernetes.io/name: {{ include "grafana-mcp-setup.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "grafana-mcp-setup.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "grafana-mcp-setup.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "grafana-mcp-setup.basePath" -}}
{{- printf "/%s" (trim (trimAll "/" .Values.basePath)) }}
{{- end }}

{{- define "grafana-mcp-setup.adminTokenSecret" -}}
{{- default (printf "%s-grafana" (include "grafana-mcp-setup.fullname" .)) .Values.grafana.existingSecret }}
{{- end }}

{{- define "grafana-mcp-setup.clientSecretName" -}}
{{- default (printf "%s-oidc" (include "grafana-mcp-setup.fullname" .)) .Values.securityPolicy.existingSecret }}
{{- end }}
