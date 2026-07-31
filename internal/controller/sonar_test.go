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
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

func TestReasonForSonarError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"unknown project", &sonarHTTPError{statusCode: http.StatusNotFound},
			qgatev1alpha1.ReasonProjectNotFound},
		{"bad token", &sonarHTTPError{statusCode: http.StatusUnauthorized},
			qgatev1alpha1.ReasonAuthenticationFailed},
		{"insufficient permissions", &sonarHTTPError{statusCode: http.StatusForbidden},
			qgatev1alpha1.ReasonAuthenticationFailed},
		{"server error", &sonarHTTPError{statusCode: http.StatusBadGateway},
			qgatev1alpha1.ReasonSonarQubeError},
		{"wrapped http error", fmt.Errorf("outer: %w", &sonarHTTPError{statusCode: http.StatusNotFound}),
			qgatev1alpha1.ReasonProjectNotFound},
		{"timeout", fmt.Errorf("request failed: %w", context.DeadlineExceeded),
			qgatev1alpha1.ReasonSonarQubeTimeout},
		{"malformed json", fmt.Errorf("decoding: %w", &json.SyntaxError{}),
			qgatev1alpha1.ReasonInvalidResponse},
		{"connection refused", errors.New("dial tcp 127.0.0.1:1: connect: connection refused"),
			qgatev1alpha1.ReasonSonarQubeUnreachable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasonForSonarError(tc.err); got != tc.want {
				t.Errorf("reasonForSonarError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestSonarHTTPClient(t *testing.T) {
	t.Run("no CA bundle reuses the shared client", func(t *testing.T) {
		got, err := sonarHTTPClient(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != defaultSonarClient {
			t.Error("expected the shared client when no CA bundle is configured")
		}
	})

	t.Run("garbage CA bundle is rejected", func(t *testing.T) {
		if _, err := sonarHTTPClient([]byte("not a certificate")); err == nil {
			t.Fatal("expected an error for a CA bundle with no PEM certificate")
		}
	})
}

// TestFetchGateStatusPrivateCA covers the self-hosted-SonarQube-behind-a-private-CA path: the same
// server must be rejected without the CA bundle and accepted with it.
func TestFetchGateStatusPrivateCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"projectStatus":{"status":"OK"}}`)
	}))
	defer server.Close()

	query := sonarQuery{server: server.URL, projectKey: "private-ca-app"}

	if _, err := fetchGateStatus(context.Background(), query); err == nil {
		t.Fatal("expected a TLS verification failure without the server's CA")
	}

	caBundle := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: server.Certificate().Raw,
	})
	query.caBundle = caBundle

	status, err := fetchGateStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("unexpected error with the server's CA trusted: %v", err)
	}
	if status != qgatev1alpha1.GateStatusOK {
		t.Errorf("got gate status %q, want %q", status, qgatev1alpha1.GateStatusOK)
	}
}

func TestFetchGateStatusRejectsResponseWithoutStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"errors":[{"msg":"Component key not found"}]}`)
	}))
	defer server.Close()

	_, err := fetchGateStatus(context.Background(), sonarQuery{server: server.URL, projectKey: "x"})
	if err == nil {
		t.Fatal("expected an error when projectStatus.status is absent")
	}
}

func TestGateStatusAge(t *testing.T) {
	now := time.Now()
	policyAt := func(gateStatus string, checked *time.Time, generation, observed int64) *qgatev1alpha1.QualityGatePolicy {
		policy := &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Generation: generation},
			Status: qgatev1alpha1.QualityGatePolicyStatus{
				GateStatus:         gateStatus,
				ObservedGeneration: observed,
			},
		}
		if checked != nil {
			t := metav1.NewTime(*checked)
			policy.Status.LastChecked = &t
		}
		return policy
	}
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	tests := []struct {
		name      string
		policy    *qgatev1alpha1.QualityGatePolicy
		wantFresh bool
	}{
		{"never checked", policyAt("", nil, 1, 1), false},
		{"status without a check time", policyAt("OK", nil, 1, 1), false},
		{"just checked", policyAt("OK", ago(time.Second), 1, 1), true},
		{"checked within the poll interval", policyAt("OK", ago(pollInterval-time.Minute), 1, 1), true},
		{"checked exactly a poll interval ago", policyAt("OK", ago(pollInterval), 1, 1), false},
		{"spec changed since the last check", policyAt("OK", ago(time.Second), 2, 1), false},
		{"check time in the future", policyAt("OK", ago(-time.Hour), 1, 1), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, fresh := gateStatusAge(tc.policy, now); fresh != tc.wantFresh {
				t.Errorf("gateStatusAge fresh = %v, want %v", fresh, tc.wantFresh)
			}
		})
	}
}

func TestPoliciesForDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := qgatev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	policy := func(name, namespace string, matchLabels map[string]string) *qgatev1alpha1.QualityGatePolicy {
		return &qgatev1alpha1.QualityGatePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: qgatev1alpha1.QualityGatePolicySpec{
				Selector: metav1.LabelSelector{MatchLabels: matchLabels},
			},
		}
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		policy("matches", "team-a", map[string]string{"app": "checkout"}),
		policy("also-matches", "team-a", map[string]string{"tier": "backend"}),
		policy("wrong-labels", "team-a", map[string]string{"app": "search"}),
		policy("wrong-namespace", "team-b", map[string]string{"app": "checkout"}),
		policy("matches-everything", "team-a", nil),
	).Build()

	r := &QualityGatePolicyReconciler{Client: fakeClient, Scheme: scheme}
	requests := r.policiesForDeployment(context.Background(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout",
			Namespace: "team-a",
			Labels:    map[string]string{"app": "checkout", "tier": "backend"},
		},
	})

	got := make(map[string]bool, len(requests))
	for _, req := range requests {
		if req.Namespace != "team-a" {
			t.Errorf("enqueued a policy outside the Deployment's namespace: %v", req)
		}
		got[req.Name] = true
	}

	// An empty selector matches everything, which is how LabelSelectorAsSelector defines it.
	for _, want := range []string{"matches", "also-matches", "matches-everything"} {
		if !got[want] {
			t.Errorf("expected policy %q to be enqueued, got %v", want, got)
		}
	}
	for _, unwanted := range []string{"wrong-labels", "wrong-namespace"} {
		if got[unwanted] {
			t.Errorf("policy %q should not have been enqueued", unwanted)
		}
	}
}
