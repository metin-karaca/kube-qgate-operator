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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

var _ = Describe("QualityGatePolicy Controller", func() {
	ctx := context.Background()
	const namespace = "default"

	reconciler := func() *QualityGatePolicyReconciler {
		return &QualityGatePolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}

	// reconcileOnce drives a single Reconcile for the named policy and returns the result.
	reconcileOnce := func(r *QualityGatePolicyReconciler, name string) (reconcile.Result, error) {
		return r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
		})
	}

	fetchPolicy := func(name string) *qgatev1alpha1.QualityGatePolicy {
		var policy qgatev1alpha1.QualityGatePolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &policy)).To(Succeed())
		return &policy
	}

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

	// createPolicy creates a policy and registers cleanup. Policies carry a finalizer once
	// reconciled, so cleanup has to wait for the controller to release it.
	createPolicy := func(policy *qgatev1alpha1.QualityGatePolicy) *qgatev1alpha1.QualityGatePolicy {
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, policy))).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				if err == nil && !policy.DeletionTimestamp.IsZero() {
					_, err = reconcileOnce(reconciler(), policy.Name)
					Expect(err).NotTo(HaveOccurred())
				}
				return apierrorsIsGone(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy))
			}, time.Second*10, time.Millisecond*50).Should(BeTrue())
		})
		return policy
	}

	// staticSonarServer serves a fixed projectStatus payload and records the last request seen.
	staticSonarServer := func(status string, seen *http.Request) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if seen != nil {
				*seen = *r.Clone(r.Context())
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"projectStatus":{"status":%q}}`, status)
		}))
		DeferCleanup(server.Close)
		return server
	}

	It("records gate status OK, sends the token, and lists matched workloads", func() {
		var seen http.Request
		server := staticSonarServer(qgatev1alpha1.GateStatusOK, &seen)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sonar-token-ok", Namespace: namespace},
			Data:       map[string][]byte{sonarTokenSecretKey: []byte("sq_test_token")},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, secret))).To(Succeed()) })

		labels := map[string]string{"app": "ok-app"}
		deployment := newDeployment("ok-app", labels)
		Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, deployment))).To(Succeed()) })

		createPolicy(&qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "ok-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:            metav1.LabelSelector{MatchLabels: labels},
				SonarServer:         server.URL,
				ProjectKey:          "ok-app",
				Mode:                qgatev1alpha1.ModeAudit,
				SonarTokenSecretRef: &corev1.LocalObjectReference{Name: "sonar-token-ok"},
			},
		})

		result, err := reconcileOnce(reconciler(), "ok-policy")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(pollInterval))

		updated := fetchPolicy("ok-policy")
		Expect(updated.Status.GateStatus).To(Equal(qgatev1alpha1.GateStatusOK))
		Expect(updated.Status.LastChecked).NotTo(BeNil())
		Expect(updated.Status.MatchedWorkloads).To(ConsistOf("ok-app"))
		Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
		Expect(updated.Finalizers).To(ContainElement(metricsFinalizer))

		username, password, ok := seen.BasicAuth()
		Expect(ok).To(BeTrue())
		Expect(username).To(Equal("sq_test_token"))
		Expect(password).To(BeEmpty())

		Expect(testutil.ToFloat64(
			qualityGateStatus.WithLabelValues(namespace, "ok-policy", "ok-app"),
		)).To(Equal(1.0))

		readyCond := meta.FindStatusCondition(updated.Status.Conditions, qgatev1alpha1.ConditionReady)
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
		Expect(readyCond.Reason).To(Equal(qgatev1alpha1.ReasonGateStatusFetched))
		Expect(readyCond.ObservedGeneration).To(Equal(updated.Generation))
	})

	It("escapes the project key rather than interpolating it into the query string", func() {
		var seen http.Request
		server := staticSonarServer(qgatev1alpha1.GateStatusOK, &seen)

		const trickyKey = `org:proj&admin=1`
		createPolicy(&qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "escape-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "escape-app"}},
				SonarServer: server.URL + "/", // trailing slash must not double up
				ProjectKey:  trickyKey,
				Mode:        qgatev1alpha1.ModeAudit,
			},
		})

		_, err := reconcileOnce(reconciler(), "escape-policy")
		Expect(err).NotTo(HaveOccurred())

		Expect(seen.URL.Path).To(Equal(sonarProjectStatusPath))
		Expect(seen.URL.Query()).To(HaveLen(1))
		Expect(seen.URL.Query().Get("projectKey")).To(Equal(trickyKey))
		Expect(seen.URL.Query().Has("admin")).To(BeFalse())
	})

	It("marks Ready=False with SonarQubeUnreachable when SonarQube cannot be reached", func() {
		createPolicy(&qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "unreachable-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "unreachable-app"}},
				SonarServer: "http://127.0.0.1:1", // nothing listens here
				ProjectKey:  "unreachable-app",
				Mode:        qgatev1alpha1.ModeAudit,
			},
		})

		result, err := reconcileOnce(reconciler(), "unreachable-policy")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(errorRetryInterval))

		updated := fetchPolicy("unreachable-policy")
		Expect(updated.Status.GateStatus).To(BeEmpty())

		readyCond := meta.FindStatusCondition(updated.Status.Conditions, qgatev1alpha1.ConditionReady)
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCond.Reason).To(Equal(qgatev1alpha1.ReasonSonarQubeUnreachable))
	})

	DescribeTable("classifies SonarQube HTTP failures into distinct condition reasons",
		func(policyName string, statusCode int, expectedReason string) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(statusCode)
			}))
			DeferCleanup(server.Close)

			createPolicy(&qgatev1alpha1.QualityGatePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: policyName, Namespace: namespace},
				Spec: qgatev1alpha1.QualityGatePolicySpec{
					Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": policyName}},
					SonarServer: server.URL,
					ProjectKey:  policyName,
					Mode:        qgatev1alpha1.ModeAudit,
				},
			})

			_, err := reconcileOnce(reconciler(), policyName)
			Expect(err).NotTo(HaveOccurred())

			readyCond := meta.FindStatusCondition(fetchPolicy(policyName).Status.Conditions,
				qgatev1alpha1.ConditionReady)
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal(expectedReason))
		},
		Entry("unknown project key", "notfound-policy",
			http.StatusNotFound, qgatev1alpha1.ReasonProjectNotFound),
		Entry("invalid token", "unauthorized-policy",
			http.StatusUnauthorized, qgatev1alpha1.ReasonAuthenticationFailed),
		Entry("insufficient permissions", "forbidden-policy",
			http.StatusForbidden, qgatev1alpha1.ReasonAuthenticationFailed),
		Entry("server-side failure", "servererror-policy",
			http.StatusInternalServerError, qgatev1alpha1.ReasonSonarQubeError),
	)

	It("reports SecretUnavailable when the referenced token Secret is missing", func() {
		server := staticSonarServer(qgatev1alpha1.GateStatusOK, nil)

		createPolicy(&qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "missing-secret-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:            metav1.LabelSelector{MatchLabels: map[string]string{"app": "missing-secret-app"}},
				SonarServer:         server.URL,
				ProjectKey:          "missing-secret-app",
				Mode:                qgatev1alpha1.ModeAudit,
				SonarTokenSecretRef: &corev1.LocalObjectReference{Name: "no-such-secret"},
			},
		})

		_, err := reconcileOnce(reconciler(), "missing-secret-policy")
		Expect(err).NotTo(HaveOccurred())

		readyCond := meta.FindStatusCondition(fetchPolicy("missing-secret-policy").Status.Conditions,
			qgatev1alpha1.ConditionReady)
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCond.Reason).To(Equal(qgatev1alpha1.ReasonSecretUnavailable))
		Expect(readyCond.Message).To(ContainSubstring("no-such-secret"))
	})

	It("reports SecretUnavailable when the token Secret exists but has no token key", func() {
		server := staticSonarServer(qgatev1alpha1.GateStatusOK, nil)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sonar-token-wrongkey", Namespace: namespace},
			Data:       map[string][]byte{"password": []byte("oops")},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, secret))).To(Succeed()) })

		createPolicy(&qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "wrongkey-secret-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:            metav1.LabelSelector{MatchLabels: map[string]string{"app": "wrongkey-app"}},
				SonarServer:         server.URL,
				ProjectKey:          "wrongkey-app",
				Mode:                qgatev1alpha1.ModeAudit,
				SonarTokenSecretRef: &corev1.LocalObjectReference{Name: "sonar-token-wrongkey"},
			},
		})

		_, err := reconcileOnce(reconciler(), "wrongkey-secret-policy")
		Expect(err).NotTo(HaveOccurred())

		readyCond := meta.FindStatusCondition(fetchPolicy("wrongkey-secret-policy").Status.Conditions,
			qgatev1alpha1.ConditionReady)
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Reason).To(Equal(qgatev1alpha1.ReasonSecretUnavailable))
		Expect(readyCond.Message).To(ContainSubstring(sonarTokenSecretKey))
	})

	It("records gate status ERROR when the quality gate fails", func() {
		server := staticSonarServer("ERROR", nil)

		createPolicy(&qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "error-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "error-app"}},
				SonarServer: server.URL,
				ProjectKey:  "error-app",
				Mode:        qgatev1alpha1.ModeAudit,
			},
		})

		_, err := reconcileOnce(reconciler(), "error-policy")
		Expect(err).NotTo(HaveOccurred())

		Expect(fetchPolicy("error-policy").Status.GateStatus).To(Equal("ERROR"))
		Expect(testutil.ToFloat64(
			qualityGateStatus.WithLabelValues(namespace, "error-policy", "error-app"),
		)).To(Equal(0.0))
	})

	It("does not re-query SonarQube while the cached status is still fresh", func() {
		var calls int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projectStatus":{"status":"OK"}}`)
		}))
		DeferCleanup(server.Close)

		createPolicy(&qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "fresh-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "fresh-app"}},
				SonarServer: server.URL,
				ProjectKey:  "fresh-app",
				Mode:        qgatev1alpha1.ModeAudit,
			},
		})

		r := reconciler()
		_, err := reconcileOnce(r, "fresh-policy")
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))

		// A Deployment event would land here; the poll interval has not elapsed, so the cached
		// status must be served without another round trip.
		result, err := reconcileOnce(r, "fresh-policy")
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(result.RequeueAfter).To(BeNumerically("<=", pollInterval))
	})

	It("removes the metric series when a policy is deleted", func() {
		server := staticSonarServer(qgatev1alpha1.GateStatusOK, nil)

		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "deleted-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "deleted-app"}},
				SonarServer: server.URL,
				ProjectKey:  "deleted-app",
				Mode:        qgatev1alpha1.ModeAudit,
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		r := reconciler()
		_, err := reconcileOnce(r, "deleted-policy")
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.CollectAndCount(qualityGateStatus, "qualitygate_status")).To(BeNumerically(">", 0))
		before := testutil.CollectAndCount(qualityGateStatus, "qualitygate_status")

		Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
		_, err = reconcileOnce(r, "deleted-policy")
		Expect(err).NotTo(HaveOccurred())

		Expect(testutil.CollectAndCount(qualityGateStatus, "qualitygate_status")).To(Equal(before - 1))
		Eventually(func() bool {
			return apierrorsIsGone(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy))
		}).Should(BeTrue())
	})

	It("drops the previous series when spec.projectKey changes", func() {
		server := staticSonarServer(qgatev1alpha1.GateStatusOK, nil)

		createPolicy(&qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "rekey-policy", Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": "rekey-app"}},
				SonarServer: server.URL,
				ProjectKey:  "old-key",
				Mode:        qgatev1alpha1.ModeAudit,
			},
		})

		r := reconciler()
		_, err := reconcileOnce(r, "rekey-policy")
		Expect(err).NotTo(HaveOccurred())
		before := testutil.CollectAndCount(qualityGateStatus, "qualitygate_status")

		policy := fetchPolicy("rekey-policy")
		policy.Spec.ProjectKey = "new-key"
		Expect(k8sClient.Update(ctx, policy)).To(Succeed())

		_, err = reconcileOnce(r, "rekey-policy")
		Expect(err).NotTo(HaveOccurred())

		// One series in, one series out — the stale "old-key" label set is gone.
		Expect(testutil.CollectAndCount(qualityGateStatus, "qualitygate_status")).To(Equal(before))
		Expect(testutil.ToFloat64(
			qualityGateStatus.WithLabelValues(namespace, "rekey-policy", "new-key"),
		)).To(Equal(1.0))
	})

	DescribeTable("emits a Warning event only in Warn and Block modes when the gate fails",
		func(policyName, mode string, expectEvent bool) {
			server := staticSonarServer("ERROR", nil)

			createPolicy(&qgatev1alpha1.QualityGatePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: policyName, Namespace: namespace},
				Spec: qgatev1alpha1.QualityGatePolicySpec{
					Selector:    metav1.LabelSelector{MatchLabels: map[string]string{"app": policyName}},
					SonarServer: server.URL,
					ProjectKey:  policyName,
					Mode:        mode,
				},
			})

			recorder := record.NewFakeRecorder(10)
			r := &QualityGatePolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: recorder}
			_, err := reconcileOnce(r, policyName)
			Expect(err).NotTo(HaveOccurred())

			if expectEvent {
				Expect(recorder.Events).To(Receive(ContainSubstring("QualityGateFailed")))
			} else {
				Expect(recorder.Events).NotTo(Receive())
			}
		},
		Entry("Warn mode", "warn-policy", qgatev1alpha1.ModeWarn, true),
		Entry("Block mode", "block-event-policy", qgatev1alpha1.ModeBlock, true),
		Entry("Audit mode", "audit-event-policy", qgatev1alpha1.ModeAudit, false),
	)
})

// apierrorsIsGone reports whether a Get failed because the object no longer exists.
func apierrorsIsGone(err error) bool {
	return client.IgnoreNotFound(err) == nil && err != nil
}
