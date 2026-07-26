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
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-apps-v1-deployment,mutating=false,failurePolicy=fail,sideEffects=None,groups=apps,resources=deployments,verbs=create;update,versions=v1,name=vdeployment.qgate.io,admissionReviewVersions=v1

// DeploymentValidator denies Deployments covered by a Block-mode QualityGatePolicy whose
// last known SonarQube quality gate result is not OK. It relies entirely on the status
// already maintained by QualityGatePolicyReconciler's periodic poll — it makes no
// synchronous call to SonarQube itself, keeping admission fast and decoupled.
type DeploymentValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &DeploymentValidator{}

// SetupDeploymentWebhookWithManager registers the validating webhook for apps/v1 Deployments.
func SetupDeploymentWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&appsv1.Deployment{}).
		WithValidator(&DeploymentValidator{Client: mgr.GetClient()}).
		Complete()
}

// ValidateCreate implements webhook.CustomValidator.
func (v *DeploymentValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return v.validate(ctx, obj)
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *DeploymentValidator) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	return v.validate(ctx, newObj)
}

// ValidateDelete implements webhook.CustomValidator. Deletions are never blocked.
func (v *DeploymentValidator) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *DeploymentValidator) validate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	deployment, ok := obj.(*appsv1.Deployment)
	if !ok {
		return nil, fmt.Errorf("expected an appsv1.Deployment but got %T", obj)
	}

	var policies qgatev1alpha1.QualityGatePolicyList
	if err := v.Client.List(ctx, &policies, client.InNamespace(deployment.Namespace)); err != nil {
		return nil, err
	}

	deploymentLabels := labels.Set(deployment.Labels)
	for _, policy := range policies.Items {
		if policy.Spec.Mode != "Block" {
			continue
		}

		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.Selector)
		if err != nil {
			return nil, err
		}
		if !selector.Matches(deploymentLabels) {
			continue
		}

		if policy.Status.GateStatus == "" {
			if policy.Spec.FailurePolicy == "Ignore" {
				continue
			}
			return nil, fmt.Errorf(
				"QualityGatePolicy %q has not yet determined a quality gate status for project %q (failurePolicy=Fail)",
				policy.Name, policy.Spec.ProjectKey)
		}

		if policy.Status.GateStatus != "OK" {
			return nil, fmt.Errorf(
				"QualityGatePolicy %q blocks this Deployment: SonarQube quality gate for project %q is %q",
				policy.Name, policy.Spec.ProjectKey, policy.Status.GateStatus)
		}
	}

	return nil, nil
}
