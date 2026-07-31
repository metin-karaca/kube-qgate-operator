/*
Copyright 2026 Metin Karaca.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

// qualityGateStatus reports the last known SonarQube quality gate result for each
// QualityGatePolicy: 1 when the gate is OK, 0 otherwise.
var qualityGateStatus = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "qualitygate_status",
		Help: "SonarQube quality gate status for a QualityGatePolicy (1 = OK, 0 = not OK).",
	},
	[]string{"namespace", "name", "project"},
)

func init() {
	metrics.Registry.MustRegister(qualityGateStatus)
}

// setGateStatusMetric publishes the gate result for a policy. Any series previously reported for
// the same policy is dropped first, so editing spec.projectKey does not leave the old project's
// series behind reporting a value that is no longer maintained.
func setGateStatusMetric(policy *qgatev1alpha1.QualityGatePolicy, gateStatus string) {
	qualityGateStatus.DeletePartialMatch(policyLabels(policy))

	value := 0.0
	if gateStatus == qgatev1alpha1.GateStatusOK {
		value = 1.0
	}
	qualityGateStatus.WithLabelValues(policy.Namespace, policy.Name, policy.Spec.ProjectKey).Set(value)
}

// deleteGateStatusMetric removes every series belonging to a policy and reports how many were
// dropped. Called on deletion so a removed policy stops appearing in Prometheus (and in alerts
// built on qualitygate_status) instead of reporting its last value indefinitely.
func deleteGateStatusMetric(policy *qgatev1alpha1.QualityGatePolicy) int {
	return qualityGateStatus.DeletePartialMatch(policyLabels(policy))
}

func policyLabels(policy *qgatev1alpha1.QualityGatePolicy) prometheus.Labels {
	return prometheus.Labels{"namespace": policy.Namespace, "name": policy.Name}
}
