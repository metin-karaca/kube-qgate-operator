{{- define "kube-qgate-operator.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{/*
Reject value combinations that would install a Deployment that cannot start. Unlike the metrics
server, controller-runtime's webhook server has no self-signed fallback: it opens a cert watcher on
--webhook-cert-path and fails if no certificate is mounted there. Catching that at template time
beats a CrashLoopBackOff whose cause is three log lines deep.
*/}}
{{- define "kube-qgate-operator.validateValues" -}}
{{- if and .Values.webhook.enabled (not .Values.certManager.enabled) -}}
{{- fail "webhook.enabled=true requires certManager.enabled=true: the Block-mode webhook server needs a mounted TLS certificate and cannot generate one. Either install cert-manager, or set webhook.enabled=false to run in Audit/Warn mode only." -}}
{{- end -}}
{{- if and .Values.webhook.enabled (lt (int .Values.webhook.timeoutSeconds) 1) -}}
{{- fail "webhook.timeoutSeconds must be between 1 and 30." -}}
{{- end -}}
{{- if and .Values.webhook.enabled (gt (int .Values.webhook.timeoutSeconds) 30) -}}
{{- fail "webhook.timeoutSeconds must be between 1 and 30." -}}
{{- end -}}
{{- if not (has .Values.webhook.failurePolicy (list "Fail" "Ignore")) -}}
{{- fail "webhook.failurePolicy must be either Fail or Ignore." -}}
{{- end -}}
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
