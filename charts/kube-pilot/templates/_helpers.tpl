{{/*
Common labels
*/}}
{{- define "kube-pilot.labels" -}}
app.kubernetes.io/name: kube-pilot
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kube-pilot.selectorLabels" -}}
app.kubernetes.io/name: kube-pilot
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Full name
*/}}
{{- define "kube-pilot.fullname" -}}
{{- printf "%s-kube-pilot" .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
