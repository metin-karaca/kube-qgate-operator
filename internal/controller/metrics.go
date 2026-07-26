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
