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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-apps-v1-deployment,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,timeoutSeconds=5,sideEffects=None,groups=apps,resources=deployments,verbs=create;update,versions=v1,name=vdeployment.qgate.io,admissionReviewVersions=v1

// DeploymentValidator denies Deployments covered by a Block-mode QualityGatePolicy whose last
// known SonarQube quality gate result is not OK. It relies entirely on the status already
// maintained by QualityGatePolicyReconciler's periodic poll — it makes no synchronous call to
// SonarQube itself, keeping admission fast and decoupled. How long a polled result stays
// trustworthy is bounded by each policy's spec.maxStaleness.
type DeploymentValidator struct {
	Client client.Client

	// Now supplies the current time when evaluating gate-status staleness. Defaults to time.Now;
	// overridden in tests.
	Now func() time.Time
}

var _ webhook.CustomValidator = &DeploymentValidator{}

// SetupDeploymentWebhookWithManager registers the validating webhook for apps/v1 Deployments.
func SetupDeploymentWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&appsv1.Deployment{}).
		WithValidator(&DeploymentValidator{Client: mgr.GetClient(), Now: time.Now}).
		Complete()
}

// ValidateCreate implements webhook.CustomValidator.
func (v *DeploymentValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return v.validate(ctx, obj)
}

// ValidateUpdate implements webhook.CustomValidator. Updates that leave both the Pod template and
// the Deployment's labels untouched are always allowed: they cannot introduce new code and cannot
// change which policies apply, so blocking them would only stop operators from scaling, pausing or
// annotating a workload while its gate happens to be red.
func (v *DeploymentValidator) ValidateUpdate(
	ctx context.Context,
	oldObj, newObj runtime.Object,
) (admission.Warnings, error) {
	oldDeployment, oldOK := oldObj.(*appsv1.Deployment)
	newDeployment, newOK := newObj.(*appsv1.Deployment)
	if oldOK && newOK &&
		apiequality.Semantic.DeepEqual(oldDeployment.Spec.Template, newDeployment.Spec.Template) &&
		apiequality.Semantic.DeepEqual(oldDeployment.Labels, newDeployment.Labels) {
		return nil, nil
	}

	return v.validate(ctx, newObj)
}

// ValidateDelete implements webhook.CustomValidator. Deletions are never blocked.
func (v *DeploymentValidator) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *DeploymentValidator) validate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	log := logf.FromContext(ctx)

	deployment, ok := obj.(*appsv1.Deployment)
	if !ok {
		return nil, fmt.Errorf("expected an appsv1.Deployment but got %T", obj)
	}

	var policies qgatev1alpha1.QualityGatePolicyList
	if err := v.Client.List(ctx, &policies, client.InNamespace(deployment.Namespace)); err != nil {
		return nil, fmt.Errorf("listing QualityGatePolicies in namespace %q: %w", deployment.Namespace, err)
	}

	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}

	deploymentLabels := labels.Set(deployment.Labels)
	for i := range policies.Items {
		policy := &policies.Items[i]
		if policy.Spec.Mode != qgatev1alpha1.ModeBlock {
			continue
		}

		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.Selector)
		if err != nil {
			// A policy nobody can evaluate must not silently stop gating. Surface it instead.
			return nil, fmt.Errorf("QualityGatePolicy %q has an invalid spec.selector: %w", policy.Name, err)
		}
		if !selector.Matches(deploymentLabels) {
			continue
		}

		usable, why := policy.UsableGateStatus(now)
		if !usable {
			if policy.Spec.FailurePolicy == qgatev1alpha1.FailurePolicyIgnore {
				log.Info("allowing Deployment despite unusable gate status (failurePolicy=Ignore)",
					"deployment", client.ObjectKeyFromObject(deployment), "policy", policy.Name, "reason", why)
				continue
			}
			log.Info("denying Deployment: no usable gate status",
				"deployment", client.ObjectKeyFromObject(deployment), "policy", policy.Name, "reason", why)
			return nil, fmt.Errorf("QualityGatePolicy %q blocks this Deployment: %s (failurePolicy=Fail)",
				policy.Name, why)
		}

		if policy.Status.GateStatus != qgatev1alpha1.GateStatusOK {
			log.Info("denying Deployment: quality gate is not OK",
				"deployment", client.ObjectKeyFromObject(deployment), "policy", policy.Name,
				"gateStatus", policy.Status.GateStatus)
			return nil, fmt.Errorf(
				"QualityGatePolicy %q blocks this Deployment: SonarQube quality gate for project %q is %q",
				policy.Name, policy.Spec.ProjectKey, policy.Status.GateStatus)
		}
	}

	return nil, nil
}
