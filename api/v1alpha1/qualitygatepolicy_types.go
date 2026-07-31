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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Modes accepted by QualityGatePolicySpec.Mode.
const (
	ModeAudit = "Audit"
	ModeWarn  = "Warn"
	ModeBlock = "Block"
)

// Values accepted by QualityGatePolicySpec.FailurePolicy.
const (
	FailurePolicyFail   = "Fail"
	FailurePolicyIgnore = "Ignore"
)

// GateStatusOK is the SonarQube project_status value meaning the quality gate passed.
const GateStatusOK = "OK"

// ConditionReady reports whether the controller currently holds a successfully fetched
// quality gate result for the policy.
const ConditionReady = "Ready"

// Reasons used on the Ready condition.
const (
	ReasonGateStatusFetched    = "GateStatusFetched"
	ReasonSonarQubeUnreachable = "SonarQubeUnreachable"
	ReasonSonarQubeTimeout     = "SonarQubeTimeout"
	ReasonSonarQubeError       = "SonarQubeError"
	ReasonProjectNotFound      = "ProjectNotFound"
	ReasonAuthenticationFailed = "AuthenticationFailed"
	ReasonInvalidResponse      = "InvalidResponse"
	ReasonSecretUnavailable    = "SecretUnavailable"
	ReasonInvalidConfiguration = "InvalidConfiguration"
)

// DefaultMaxStaleness applies when Spec.MaxStaleness is unset — which normally cannot happen
// because the CRD defaults it, but does for objects persisted before the field existed.
const DefaultMaxStaleness = 30 * time.Minute

// QualityGatePolicySpec defines the desired state of QualityGatePolicy.
type QualityGatePolicySpec struct {
	// Selector, aims which deployment does this policy addresses.
	Selector metav1.LabelSelector `json:"selector"`

	// SonarServer, represents SonarQube's base URL, e.g. https://sonarqube.example.com.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^https?://`
	SonarServer string `json:"sonarServer"`

	// ProjectKey is the project key on SonarQube.
	// +kubebuilder:validation:MinLength=1
	ProjectKey string `json:"projectKey"`

	// Mode, represents how the gate result is applied: Audit, Warn, or Block.
	// +kubebuilder:validation:Enum=Audit;Warn;Block
	// +kubebuilder:default=Audit
	Mode string `json:"mode,omitempty"`

	// SonarTokenSecretRef references a Secret (in the same namespace) whose "token" key holds
	// the SonarQube user token to use for authenticated requests. Omit for anonymous access.
	SonarTokenSecretRef *corev1.LocalObjectReference `json:"sonarTokenSecretRef,omitempty"`

	// SonarCASecretRef references a Secret (in the same namespace) whose "ca.crt" key holds a
	// PEM-encoded CA bundle used to verify the SonarQube server's TLS certificate. Set this for
	// a self-hosted SonarQube whose certificate is signed by a private CA. Omit to use the
	// system trust store.
	SonarCASecretRef *corev1.LocalObjectReference `json:"sonarCASecretRef,omitempty"`

	// FailurePolicy governs Block-mode behavior when no usable gate status is available —
	// either because none has ever been determined, or because the last one is older than
	// MaxStaleness. Fail denies the request (fail-closed); Ignore allows it (fail-open).
	// +kubebuilder:validation:Enum=Fail;Ignore
	// +kubebuilder:default=Fail
	FailurePolicy string `json:"failurePolicy,omitempty"`

	// MaxStaleness bounds how old a successfully fetched gate status may be and still be
	// trusted by the Block-mode admission webhook. Once Status.LastChecked is older than this,
	// the webhook stops trusting Status.GateStatus and applies FailurePolicy instead — without
	// it, an unreachable SonarQube would leave a stale "OK" admitting Deployments forever.
	// Set to 0 to disable the staleness check.
	// +kubebuilder:default="30m"
	MaxStaleness *metav1.Duration `json:"maxStaleness,omitempty"`
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

	// ObservedGeneration is the .metadata.generation the controller last reconciled, so clients
	// can tell whether the reported status reflects the current spec.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.projectKey`
// +kubebuilder:printcolumn:name="Gate",type=string,JSONPath=`.status.gateStatus`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last Checked",type=date,JSONPath=`.status.lastChecked`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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

// ResolvedMaxStaleness returns the staleness bound to enforce for this policy. A zero return
// value means the staleness check is disabled.
func (p *QualityGatePolicy) ResolvedMaxStaleness() time.Duration {
	if p.Spec.MaxStaleness == nil {
		return DefaultMaxStaleness
	}
	return p.Spec.MaxStaleness.Duration
}

// UsableGateStatus reports whether Status.GateStatus can be trusted as of now. When it cannot,
// the returned string explains why, ready to be embedded in an admission denial message.
func (p *QualityGatePolicy) UsableGateStatus(now time.Time) (bool, string) {
	if p.Status.GateStatus == "" {
		return false, "the controller has not yet determined a quality gate status for project " +
			quoted(p.Spec.ProjectKey)
	}

	maxStaleness := p.ResolvedMaxStaleness()
	if maxStaleness <= 0 {
		return true, ""
	}

	if p.Status.LastChecked == nil {
		return false, "the quality gate status for project " + quoted(p.Spec.ProjectKey) +
			" has no recorded check time, so its age cannot be verified"
	}

	if age := now.Sub(p.Status.LastChecked.Time); age > maxStaleness {
		return false, "the last successful quality gate check for project " + quoted(p.Spec.ProjectKey) +
			" was " + age.Truncate(time.Second).String() + " ago, exceeding maxStaleness " + maxStaleness.String()
	}

	return true, ""
}

func quoted(s string) string {
	return `"` + s + `"`
}

func init() {
	SchemeBuilder.Register(&QualityGatePolicy{}, &QualityGatePolicyList{})
}
