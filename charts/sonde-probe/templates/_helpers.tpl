{{- define "sonde-probe.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "sonde-probe.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "sonde-probe.labels" -}}
app.kubernetes.io/name: {{ include "sonde-probe.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "sonde-probe.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sonde-probe.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "sonde-probe.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "sonde-probe.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "sonde-probe.enrolmentSecretName" -}}
{{- if .Values.enrolment.existingSecret -}}
{{- .Values.enrolment.existingSecret -}}
{{- else -}}
{{- printf "%s-enrolment" (include "sonde-probe.fullname" .) -}}
{{- end -}}
{{- end -}}
