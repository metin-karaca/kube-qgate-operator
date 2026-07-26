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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

var _ = Describe("QualityGatePolicy Controller", func() {
	ctx := context.Background()
	const namespace = "default"

	reconciler := func() *QualityGatePolicyReconciler {
		return &QualityGatePolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}

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

	It("records gate status OK, sends the token, and lists matched workloads", func() {
		var gotAuthHeader string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuthHeader = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"projectStatus":{"status":"OK"}}`)
		}))
		defer server.Close()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sonar-token-ok", Namespace: namespace},
			Data:       map[string][]byte{"token": []byte("sq_test_token")},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		defer k8sClient.Delete(ctx, secret)

		labels := map[string]string{"app": "ok-app"}
		deployment := newDeployment("ok-app", labels)
		Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
		defer k8sClient.Delete(ctx, deployment)

		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "ok-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:            metav1.LabelSelector{MatchLabels: labels},
				SonarServer:         server.URL,
				ProjectKey:          "ok-app",
				Mode:                "Audit",
				SonarTokenSecretRef: &corev1.LocalObjectReference{Name: "sonar-token-ok"},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		_, err := reconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: policy.Name, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		var updated qgatev1alpha1.QualityGatePolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: namespace}, &updated)).To(Succeed())

		Expect(updated.Status.GateStatus).To(Equal("OK"))
		Expect(updated.Status.LastChecked).NotTo(BeNil())
		Expect(updated.Status.MatchedWorkloads).To(ConsistOf("ok-app"))
		Expect(gotAuthHeader).NotTo(BeEmpty())

		readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
		Expect(readyCond.Reason).To(Equal("GateStatusFetched"))
	})

	It("marks Ready=False with SonarQubeUnreachable when SonarQube cannot be reached", func() {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "unreachable-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "unreachable-app"}},
				SonarServer: "http://127.0.0.1:1", // nothing listens here
				ProjectKey:  "unreachable-app",
				Mode:        "Audit",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		result, err := reconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: policy.Name, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		var updated qgatev1alpha1.QualityGatePolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: namespace}, &updated)).To(Succeed())

		Expect(updated.Status.GateStatus).To(BeEmpty())
		readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCond.Reason).To(Equal("SonarQubeUnreachable"))
	})

	It("records gate status ERROR when the quality gate fails", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"projectStatus":{"status":"ERROR"}}`)
		}))
		defer server.Close()

		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "error-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "error-app"}},
				SonarServer: server.URL,
				ProjectKey:  "error-app",
				Mode:        "Audit",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		_, err := reconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: policy.Name, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		var updated qgatev1alpha1.QualityGatePolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: namespace}, &updated)).To(Succeed())
		Expect(updated.Status.GateStatus).To(Equal("ERROR"))
	})

	It("emits a Warning event in Warn mode when the gate fails, and none in Audit mode", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"projectStatus":{"status":"ERROR"}}`)
		}))
		defer server.Close()

		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "warn-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "warn-app"}},
				SonarServer: server.URL,
				ProjectKey:  "warn-app",
				Mode:        "Warn",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer k8sClient.Delete(ctx, policy)

		recorder := record.NewFakeRecorder(10)
		r := &QualityGatePolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: recorder}
		_, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: policy.Name, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(recorder.Events).To(Receive(ContainSubstring("QualityGateFailed")))

		auditPolicy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "warn-audit-control", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "warn-audit-app"}},
				SonarServer: server.URL,
				ProjectKey:  "warn-audit-app",
				Mode:        "Audit",
			},
		}
		Expect(k8sClient.Create(ctx, auditPolicy)).To(Succeed())
		defer k8sClient.Delete(ctx, auditPolicy)

		auditRecorder := record.NewFakeRecorder(10)
		ar := &QualityGatePolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: auditRecorder}
		_, err = ar.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: auditPolicy.Name, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(auditRecorder.Events).NotTo(Receive())
	})
})
