{{- define "kube-qgate-operator.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "kube-qgate-operator.fullname" -}}
{{- .Release.Name -}}
{{- end -}}

{{- define "kube-qgate-operator.labels" -}}
app.kubernetes.io/name: {{ include "kube-qgate-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "kube-qgate-operator.selectorLabels" -}}
control-plane: controller-manager
app.kubernetes.io/name: {{ include "kube-qgate-operator.name" . }}
{{- end -}}

{{- define "kube-qgate-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kube-qgate-operator.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
