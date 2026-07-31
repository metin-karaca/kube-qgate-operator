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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

const (
	// sonarTokenSecretKey is the fixed Secret data key holding the SonarQube user token.
	sonarTokenSecretKey = "token"
	// sonarCASecretKey is the fixed Secret data key holding a PEM CA bundle for SonarQube.
	sonarCASecretKey = "ca.crt"

	// metricsFinalizer lets the controller drop a policy's Prometheus series before the object
	// disappears; without it, deleted policies would keep reporting a gate status forever.
	metricsFinalizer = "qgate.qgate.io/metrics-cleanup"

	// pollInterval is how often a healthy policy re-queries SonarQube.
	pollInterval = 5 * time.Minute
	// errorRetryInterval is how soon a policy is retried after a failed SonarQube query.
	errorRetryInterval = time.Minute
	// sonarRequestTimeout bounds a single SonarQube HTTP request.
	sonarRequestTimeout = 10 * time.Second

	// sonarProjectStatusPath is the SonarQube web API endpoint reporting a project's gate result.
	sonarProjectStatusPath = "/api/qualitygates/project_status"
)

// QualityGatePolicyReconciler reconciles a QualityGatePolicy object
type QualityGatePolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=qgate.qgate.io,resources=qualitygatepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=qgate.qgate.io,resources=qualitygatepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=qgate.qgate.io,resources=qualitygatepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile refreshes a QualityGatePolicy's observed state: which Deployments its selector
// currently matches, and the latest SonarQube quality gate result for its project. The result is
// published three ways — on the object's status (which the Block-mode admission webhook reads),
// as the qualitygate_status Prometheus gauge, and, in Warn and Block modes, as a Kubernetes Event
// when the gate is not OK.
//
// SonarQube is only queried when the cached status is older than pollInterval, so the extra
// reconciles triggered by Deployment churn stay cheap.
func (r *QualityGatePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var policy qgatev1alpha1.QualityGatePolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &policy)
	}

	if !controllerutil.ContainsFinalizer(&policy, metricsFinalizer) {
		controllerutil.AddFinalizer(&policy, metricsFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.Selector)
	if err != nil {
		// A malformed selector is a spec problem: report it and wait for the user to fix it
		// rather than retrying a request that can never succeed.
		log.Error(err, "policy selector is invalid")
		return r.setNotReady(ctx, &policy, qgatev1alpha1.ReasonInvalidConfiguration,
			fmt.Errorf("invalid spec.selector: %w", err), 0)
	}

	matched, err := r.matchedDeployments(ctx, policy.Namespace, selector)
	if err != nil {
		return ctrl.Result{}, err
	}
	policy.Status.MatchedWorkloads = matched

	// Serve the cached gate status while it is still fresh: Deployment events can requeue a
	// policy far more often than SonarQube should be polled.
	if age, fresh := gateStatusAge(&policy, time.Now()); fresh {
		log.V(1).Info("gate status still fresh, skipping SonarQube query",
			"gateStatus", policy.Status.GateStatus, "age", age.Truncate(time.Second))
		policy.Status.ObservedGeneration = policy.Generation
		if err := r.Status().Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: pollInterval - age}, nil
	}

	credentials, err := r.sonarCredentials(ctx, &policy)
	if err != nil {
		log.Error(err, "unable to resolve SonarQube credentials")
		return r.setNotReady(ctx, &policy, qgatev1alpha1.ReasonSecretUnavailable, err, errorRetryInterval)
	}

	gateStatus, err := fetchGateStatus(ctx, sonarQuery{
		server:     policy.Spec.SonarServer,
		projectKey: policy.Spec.ProjectKey,
		token:      credentials.token,
		caBundle:   credentials.caBundle,
	})
	if err != nil {
		reason := reasonForSonarError(err)
		log.Error(err, "unable to fetch quality gate status", "reason", reason,
			"sonarServer", policy.Spec.SonarServer, "projectKey", policy.Spec.ProjectKey)
		return r.setNotReady(ctx, &policy, reason, err, errorRetryInterval)
	}

	log.Info("fetched quality gate status", "gateStatus", gateStatus,
		"projectKey", policy.Spec.ProjectKey, "matchedWorkloads", len(matched))

	now := metav1.Now()
	policy.Status.GateStatus = gateStatus
	policy.Status.LastChecked = &now
	policy.Status.ObservedGeneration = policy.Generation
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               qgatev1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             qgatev1alpha1.ReasonGateStatusFetched,
		Message:            fmt.Sprintf("last SonarQube gate status: %s", gateStatus),
		ObservedGeneration: policy.Generation,
	})

	setGateStatusMetric(&policy, gateStatus)

	if gateStatus != qgatev1alpha1.GateStatusOK && r.Recorder != nil &&
		(policy.Spec.Mode == qgatev1alpha1.ModeWarn || policy.Spec.Mode == qgatev1alpha1.ModeBlock) {
		r.Recorder.Eventf(&policy, corev1.EventTypeWarning, "QualityGateFailed",
			"SonarQube quality gate status for project %q is %q", policy.Spec.ProjectKey, gateStatus)
	}

	if err := r.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

// reconcileDelete drops the policy's metric series and releases the finalizer.
func (r *QualityGatePolicyReconciler) reconcileDelete(
	ctx context.Context,
	policy *qgatev1alpha1.QualityGatePolicy,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(policy, metricsFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("policy is being deleted, cleaning up metric series",
		"seriesRemoved", deleteGateStatusMetric(policy))

	controllerutil.RemoveFinalizer(policy, metricsFinalizer)
	if err := r.Update(ctx, policy); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// setNotReady records a Ready=False condition with the given reason and requeues. It deliberately
// leaves Status.GateStatus untouched so a transient SonarQube outage does not erase the last known
// result; how long that result stays trustworthy is bounded by Spec.MaxStaleness instead.
func (r *QualityGatePolicyReconciler) setNotReady(
	ctx context.Context,
	policy *qgatev1alpha1.QualityGatePolicy,
	reason string,
	cause error,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               qgatev1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            cause.Error(),
		ObservedGeneration: policy.Generation,
	})
	policy.Status.ObservedGeneration = policy.Generation

	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// matchedDeployments returns the names of Deployments in the namespace matched by the selector.
func (r *QualityGatePolicyReconciler) matchedDeployments(
	ctx context.Context,
	namespace string,
	selector labels.Selector,
) ([]string, error) {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return nil, err
	}

	matched := make([]string, 0, len(deployments.Items))
	for _, d := range deployments.Items {
		matched = append(matched, d.Name)
	}
	return matched, nil
}

// sonarCredentials holds the optional authentication and TLS material for SonarQube requests.
type sonarCredentials struct {
	token    string
	caBundle []byte
}

func (r *QualityGatePolicyReconciler) sonarCredentials(
	ctx context.Context,
	policy *qgatev1alpha1.QualityGatePolicy,
) (sonarCredentials, error) {
	var credentials sonarCredentials

	if ref := policy.Spec.SonarTokenSecretRef; ref != nil {
		token, err := r.secretValue(ctx, policy.Namespace, ref.Name, sonarTokenSecretKey)
		if err != nil {
			return credentials, fmt.Errorf("resolving sonarTokenSecretRef: %w", err)
		}
		credentials.token = string(token)
	}

	if ref := policy.Spec.SonarCASecretRef; ref != nil {
		caBundle, err := r.secretValue(ctx, policy.Namespace, ref.Name, sonarCASecretKey)
		if err != nil {
			return credentials, fmt.Errorf("resolving sonarCASecretRef: %w", err)
		}
		credentials.caBundle = caBundle
	}

	return credentials, nil
}

// secretValue reads one key out of a Secret, treating a missing Secret or a missing/empty key as
// an error so the cause lands on the policy's Ready condition instead of silently degrading to an
// anonymous request or the system trust store.
func (r *QualityGatePolicyReconciler) secretValue(
	ctx context.Context,
	namespace, name, key string,
) ([]byte, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("secret %q not found in namespace %q", name, namespace)
		}
		return nil, err
	}

	value, ok := secret.Data[key]
	if !ok || len(value) == 0 {
		return nil, fmt.Errorf("secret %q has no non-empty %q key", name, key)
	}
	return value, nil
}

// SetupWithManager sets up the controller with the Manager. Deployments are watched so
// Status.MatchedWorkloads keeps up with workloads appearing, disappearing or being relabelled
// instead of lagging by up to a poll interval; status-only Deployment updates are filtered out.
func (r *QualityGatePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&qgatev1alpha1.QualityGatePolicy{}).
		Watches(
			&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(r.policiesForDeployment),
			builder.WithPredicates(predicate.LabelChangedPredicate{}),
		).
		Named("qualitygatepolicy").
		Complete(r)
}

// policiesForDeployment maps a Deployment to the policies in its namespace whose selector matches
// it, so their MatchedWorkloads can be refreshed.
func (r *QualityGatePolicyReconciler) policiesForDeployment(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	log := logf.FromContext(ctx)

	var policies qgatev1alpha1.QualityGatePolicyList
	if err := r.List(ctx, &policies, client.InNamespace(obj.GetNamespace())); err != nil {
		log.Error(err, "unable to list QualityGatePolicies for Deployment event",
			"deployment", client.ObjectKeyFromObject(obj))
		return nil
	}

	deploymentLabels := labels.Set(obj.GetLabels())
	requests := make([]reconcile.Request, 0, len(policies.Items))
	for i := range policies.Items {
		policy := &policies.Items[i]
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.Selector)
		if err != nil || !selector.Matches(deploymentLabels) {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(policy)})
	}
	return requests
}

// gateStatusAge reports how old the cached gate status is and whether it is still fresh enough to
// serve without re-querying SonarQube.
func gateStatusAge(policy *qgatev1alpha1.QualityGatePolicy, now time.Time) (time.Duration, bool) {
	if policy.Status.GateStatus == "" || policy.Status.LastChecked == nil {
		return 0, false
	}
	if policy.Generation != policy.Status.ObservedGeneration {
		// The spec changed and may now point at a different server or project.
		return 0, false
	}
	age := now.Sub(policy.Status.LastChecked.Time)
	return age, age >= 0 && age < pollInterval
}

// sonarQuery describes a single SonarQube quality gate lookup.
type sonarQuery struct {
	server     string
	projectKey string
	token      string
	caBundle   []byte
}

type sonarQubeResponse struct {
	ProjectStatus struct {
		Status string `json:"status"`
	} `json:"projectStatus"`
}

// sonarHTTPError is returned when SonarQube answers with a non-200 status, so the caller can map
// the code onto a meaningful condition reason.
type sonarHTTPError struct {
	statusCode int
}

func (e *sonarHTTPError) Error() string {
	return fmt.Sprintf("sonarqube returned HTTP %d (%s)", e.statusCode, http.StatusText(e.statusCode))
}

// reasonForSonarError classifies a failed lookup so that the two most common misconfigurations —
// a wrong projectKey and a bad token — are not both reported as "unreachable".
func reasonForSonarError(err error) string {
	var httpErr *sonarHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.statusCode {
		case http.StatusNotFound:
			return qgatev1alpha1.ReasonProjectNotFound
		case http.StatusUnauthorized, http.StatusForbidden:
			return qgatev1alpha1.ReasonAuthenticationFailed
		default:
			return qgatev1alpha1.ReasonSonarQubeError
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return qgatev1alpha1.ReasonSonarQubeTimeout
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return qgatev1alpha1.ReasonInvalidResponse
	}
	return qgatev1alpha1.ReasonSonarQubeUnreachable
}

// defaultSonarClient is shared by every policy that does not pin a custom CA, so connections are
// pooled across reconciles.
var defaultSonarClient = &http.Client{Timeout: sonarRequestTimeout}

// sonarHTTPClient returns a client trusting the given PEM CA bundle, or the shared client when no
// bundle is configured.
func sonarHTTPClient(caBundle []byte) (*http.Client, error) {
	if len(caBundle) == 0 {
		return defaultSonarClient, nil
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBundle) {
		return nil, fmt.Errorf("CA bundle contains no valid PEM certificate")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: sonarRequestTimeout, Transport: transport}, nil
}

func fetchGateStatus(ctx context.Context, q sonarQuery) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, sonarRequestTimeout)
	defer cancel()

	endpoint, err := url.Parse(strings.TrimRight(q.server, "/") + sonarProjectStatusPath)
	if err != nil {
		return "", fmt.Errorf("invalid sonarServer %q: %w", q.server, err)
	}
	// Encode rather than interpolate: project keys legitimately contain characters such as ':'
	// and '+', and an unescaped '&' would otherwise inject query parameters.
	endpoint.RawQuery = url.Values{"projectKey": []string{q.projectKey}}.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Accept", "application/json")
	if q.token != "" {
		// SonarQube takes the token as the basic-auth username with an empty password.
		httpReq.SetBasicAuth(q.token, "")
	}

	httpClient, err := sonarHTTPClient(q.caBundle)
	if err != nil {
		return "", err
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", &sonarHTTPError{statusCode: resp.StatusCode}
	}

	var body sonarQubeResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decoding SonarQube response: %w", err)
	}
	if body.ProjectStatus.Status == "" {
		return "", fmt.Errorf("SonarQube response contained no projectStatus.status")
	}

	return body.ProjectStatus.Status, nil
}
