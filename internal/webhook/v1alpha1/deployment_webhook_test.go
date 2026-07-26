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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
						Containers: []corev1.Container{{Name: "app", Image: "nginx:latest"}},
					},
				},
			},
		}
	}

	It("denies a Deployment matched by a Block policy whose gate status is not OK", func() {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "block-error-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "blocked-app"}},
				SonarServer: "http://sonar.example.invalid",
				ProjectKey:  "blocked-app",
				Mode:        "Block",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		policy.Status.GateStatus = "ERROR"
		Expect(k8sClient.Status().Update(ctx, policy)).To(Succeed())

		deployment := newDeployment("blocked-app", map[string]string{"app": "blocked-app"})
		err := k8sClient.Create(ctx, deployment)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("blocks this Deployment"))
	})

	It("allows a Deployment matched by a Block policy whose gate status is OK", func() {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "block-ok-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "allowed-app"}},
				SonarServer: "http://sonar.example.invalid",
				ProjectKey:  "allowed-app",
				Mode:        "Block",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		policy.Status.GateStatus = "OK"
		Expect(k8sClient.Status().Update(ctx, policy)).To(Succeed())

		deployment := newDeployment("allowed-app", map[string]string{"app": "allowed-app"})
		Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
		defer k8sClient.Delete(ctx, deployment)
	})

	It("denies a Deployment when the policy's gate status is unknown and failurePolicy is Fail (default)", func() {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "block-unknown-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "unknown-app"}},
				SonarServer: "http://sonar.example.invalid",
				ProjectKey:  "unknown-app",
				Mode:        "Block",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		deployment := newDeployment("unknown-app", map[string]string{"app": "unknown-app"})
		err := k8sClient.Create(ctx, deployment)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("has not yet determined"))
	})

	It("allows a Deployment when the policy's gate status is unknown and failurePolicy is Ignore", func() {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "block-unknown-ignore-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:      metav1.LabelSelector{MatchLabels: map[string]string{"app": "unknown-ignore-app"}},
				SonarServer:   "http://sonar.example.invalid",
				ProjectKey:    "unknown-ignore-app",
				Mode:          "Block",
				FailurePolicy: "Ignore",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		deployment := newDeployment("unknown-ignore-app", map[string]string{"app": "unknown-ignore-app"})
		Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
		defer k8sClient.Delete(ctx, deployment)
	})

	It("does not block a Deployment covered only by an Audit-mode policy", func() {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "audit-policy-webhook", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "audit-app"}},
				SonarServer: "http://sonar.example.invalid",
				ProjectKey:  "audit-app",
				Mode:        "Audit",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		deployment := newDeployment("audit-app", map[string]string{"app": "audit-app"})
		Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
		defer k8sClient.Delete(ctx, deployment)
	})
})
