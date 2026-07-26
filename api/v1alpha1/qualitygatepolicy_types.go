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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// QualityGatePolicySpec defines the desired state of QualityGatePolicy.
type QualityGatePolicySpec struct {
	// Selector, aims which deployment does this policy addresses.
	Selector metav1.LabelSelector `json:"selector"`

	// SonarServer, represents SonarQube's base URL.
	SonarServer string `json:"sonarServer"`

	// ProjectKey is the project key on SonarQube.
	ProjectKey string `json:"projectKey"`

	// Mode, represents how the gate result is applied: Audit, Warn, or Block.
	// +kubebuilder:validation:Enum=Audit;Warn;Block
	// +kubebuilder:default=Audit
	Mode string `json:"mode,omitempty"`

	// SonarTokenSecretRef references a Secret (in the same namespace) whose "token" key holds
	// the SonarQube user token to use for authenticated requests. Omit for anonymous access.
	SonarTokenSecretRef *corev1.LocalObjectReference `json:"sonarTokenSecretRef,omitempty"`

	// FailurePolicy governs Block-mode behavior when the gate status has never been
	// successfully determined yet. Fail denies the request (fail-closed); Ignore allows it
	// (fail-open).
	// +kubebuilder:validation:Enum=Fail;Ignore
	// +kubebuilder:default=Fail
	FailurePolicy string `json:"failurePolicy,omitempty"`
}

// QualityGatePolicyStatus defines the observed state of QualityGatePolicy.
type QualityGatePolicyStatus struct {
	// GateStatus is the latest quality gate result fetched from SonarQube (OK, ERROR, Unknown).
	GateStatus string `json:"gateStatus,omitempty"`

	// LastChecked is the time when the controller last queried SonarQube.
	LastChecked *metav1.Time `json:"lastChecked,omitempty"`

	// Conditions holds the observed status of this policy in the standard Kubernetes condition format.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// MatchedWorkloads lists the names of Deployments currently matched by Spec.Selector.
	MatchedWorkloads []string `json:"matchedWorkloads,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// QualityGatePolicy is the Schema for the qualitygatepolicies API.
type QualityGatePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QualityGatePolicySpec   `json:"spec,omitempty"`
	Status QualityGatePolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// QualityGatePolicyList contains a list of QualityGatePolicy.
type QualityGatePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QualityGatePolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&QualityGatePolicy{}, &QualityGatePolicyList{})
}
