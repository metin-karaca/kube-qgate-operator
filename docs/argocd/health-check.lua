-- Custom ArgoCD health check for QualityGatePolicy (qgate.qgate.io).
--
-- Maps the operator's own Ready condition and GateStatus onto ArgoCD's health
-- states, so "argocd app get" / the UI show whether a workload's SonarQube
-- quality gate is currently passing without needing to read status.conditions
-- by hand.
--
-- Install by copying this script into the argocd-cm ConfigMap under the key
-- resource.customizations.health.qgate.qgate.io_QualityGatePolicy (see
-- argocd-cm-patch.yaml in this directory for a ready-to-apply example).

hs = {}

if obj.status ~= nil then
  if obj.status.conditions ~= nil then
    for i, condition in ipairs(obj.status.conditions) do
      if condition.type == "Ready" and condition.status == "False" then
        hs.status = "Degraded"
        hs.message = condition.message
        return hs
      end
    end
  end

  if obj.status.gateStatus == "OK" then
    hs.status = "Healthy"
    hs.message = "SonarQube quality gate is OK"
    return hs
  elseif obj.status.gateStatus ~= nil and obj.status.gateStatus ~= "" then
    hs.status = "Degraded"
    hs.message = "SonarQube quality gate is " .. obj.status.gateStatus
    return hs
  end
end

hs.status = "Progressing"
hs.message = "Waiting for the controller to determine the quality gate status"
return hs
