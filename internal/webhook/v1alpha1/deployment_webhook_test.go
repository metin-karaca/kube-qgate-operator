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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

var _ = Describe("Deployment validating webhook", func() {
	const namespace = "default"

	newDeployment := func(name string, labels map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}},
					},
				},
			},
		}
	}

	// newPolicy creates a Block-mode policy and, when gateStatus is non-empty, publishes it as
	// having been checked checkedAgo in the past.
	newPolicy := func(name, app, gateStatus string, checkedAgo time.Duration) *qgatev1alpha1.QualityGatePolicy {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
				SonarServer: "http://sonar.example.invalid",
				ProjectKey:  app,
				Mode:        qgatev1alpha1.ModeBlock,
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, policy))).To(Succeed())
		})

		if gateStatus != "" {
			checked := metav1.NewTime(time.Now().Add(-checkedAgo))
			policy.Status.GateStatus = gateStatus
			policy.Status.LastChecked = &checked
			Expect(k8sClient.Status().Update(ctx, policy)).To(Succeed())
		}
		return policy
	}

	createDeployment := func(deployment *appsv1.Deployment) error {
		err := k8sClient.Create(ctx, deployment)
		if err == nil {
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, deployment))).To(Succeed())
			})
		}
		return err
	}

	It("denies a Deployment matched by a Block policy whose gate status is not OK", func() {
		newPolicy("block-error-policy", "blocked-app", "ERROR", time.Minute)

		err := createDeployment(newDeployment("blocked-app", map[string]string{"app": "blocked-app"}))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("blocks this Deployment"))
		Expect(err.Error()).To(ContainSubstring(`is "ERROR"`))
	})

	It("allows a Deployment matched by a Block policy whose gate status is OK", func() {
		newPolicy("block-ok-policy", "allowed-app", qgatev1alpha1.GateStatusOK, time.Minute)

		Expect(createDeployment(newDeployment("allowed-app", map[string]string{"app": "allowed-app"}))).To(Succeed())
	})

	It("denies a Deployment when the policy's gate status is unknown and failurePolicy is Fail (default)", func() {
		newPolicy("block-unknown-policy", "unknown-app", "", 0)

		err := createDeployment(newDeployment("unknown-app", map[string]string{"app": "unknown-app"}))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("has not yet determined"))
	})

	It("allows a Deployment when the policy's gate status is unknown and failurePolicy is Ignore", func() {
		policy := newPolicy("block-unknown-ignore-policy", "unknown-ignore-app", "", 0)
		policy.Spec.FailurePolicy = qgatev1alpha1.FailurePolicyIgnore
		Expect(k8sClient.Update(ctx, policy)).To(Succeed())

		Expect(createDeployment(
			newDeployment("unknown-ignore-app", map[string]string{"app": "unknown-ignore-app"}),
		)).To(Succeed())
	})

	It("denies a Deployment when an OK gate status is older than maxStaleness", func() {
		// Default maxStaleness is 30m, so a check from an hour ago must not be trusted even
		// though it said OK — otherwise an unreachable SonarQube would admit forever.
		newPolicy("block-stale-policy", "stale-app", qgatev1alpha1.GateStatusOK, time.Hour)

		err := createDeployment(newDeployment("stale-app", map[string]string{"app": "stale-app"}))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exceeding maxStaleness"))
	})

	It("allows a stale OK gate status when the policy disables the staleness check", func() {
		policy := newPolicy("block-nostale-policy", "nostale-app", qgatev1alpha1.GateStatusOK, time.Hour)
		policy.Spec.MaxStaleness = &metav1.Duration{Duration: 0}
		Expect(k8sClient.Update(ctx, policy)).To(Succeed())

		Expect(createDeployment(
			newDeployment("nostale-app", map[string]string{"app": "nostale-app"}),
		)).To(Succeed())
	})

	It("allows a stale gate status when failurePolicy is Ignore", func() {
		policy := newPolicy("block-stale-ignore-policy", "stale-ignore-app", qgatev1alpha1.GateStatusOK, time.Hour)
		policy.Spec.FailurePolicy = qgatev1alpha1.FailurePolicyIgnore
		Expect(k8sClient.Update(ctx, policy)).To(Succeed())

		Expect(createDeployment(
			newDeployment("stale-ignore-app", map[string]string{"app": "stale-ignore-app"}),
		)).To(Succeed())
	})

	It("does not block a Deployment covered only by an Audit-mode policy", func() {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "audit-policy-webhook", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "audit-app"}},
				SonarServer: "http://sonar.example.invalid",
				ProjectKey:  "audit-app",
				Mode:        qgatev1alpha1.ModeAudit,
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, policy))).To(Succeed())
		})

		Expect(createDeployment(newDeployment("audit-app", map[string]string{"app": "audit-app"}))).To(Succeed())
	})

	Context("updates to an already-admitted Deployment", func() {
		const app = "update-app"
		var deployment *appsv1.Deployment

		BeforeEach(func() {
			policy := newPolicy("block-update-policy", app, qgatev1alpha1.GateStatusOK, time.Minute)

			deployment = newDeployment(app, map[string]string{"app": app})
			Expect(createDeployment(deployment)).To(Succeed())

			// The gate turns red after the workload is already running.
			checked := metav1.NewTime(time.Now())
			policy.Status.GateStatus = "ERROR"
			policy.Status.LastChecked = &checked
			Expect(k8sClient.Status().Update(ctx, policy)).To(Succeed())
		})

		It("allows a scale-up, which cannot introduce new code", func() {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app, Namespace: namespace}, deployment)).To(Succeed())
			replicas := int32(3)
			deployment.Spec.Replicas = &replicas
			Expect(k8sClient.Update(ctx, deployment)).To(Succeed())
		})

		It("denies a Pod template change", func() {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app, Namespace: namespace}, deployment)).To(Succeed())
			deployment.Spec.Template.Spec.Containers[0].Image = "nginx:1.28"
			err := k8sClient.Update(ctx, deployment)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("blocks this Deployment"))
		})
	})

	It("re-evaluates a Deployment whose labels change to match a Block policy", func() {
		newPolicy("block-relabel-policy", "relabel-app", "ERROR", time.Minute)

		deployment := newDeployment("relabel-target", map[string]string{"app": "unmatched-app"})
		Expect(createDeployment(deployment)).To(Succeed())

		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "relabel-target", Namespace: namespace}, deployment)).To(Succeed())
		deployment.Labels["app"] = "relabel-app"
		err := k8sClient.Update(ctx, deployment)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("blocks this Deployment"))
	})
})
