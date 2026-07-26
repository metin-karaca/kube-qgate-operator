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
	"fmt"
	"net/http"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	qgatev1alpha1 "github.com/metin-karaca/kube-qgate-operator/api/v1alpha1"
)

// sonarTokenSecretKey is the fixed Secret data key holding the SonarQube user token.
const sonarTokenSecretKey = "token"

// QualityGatePolicyReconciler reconciles a QualityGatePolicy object
type QualityGatePolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=qgate.qgate.io,resources=qualitygatepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=qgate.qgate.io,resources=qualitygatepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=qgate.qgate.io,resources=qualitygatepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the QualityGatePolicy object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *QualityGatePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	var policy qgatev1alpha1.QualityGatePolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.Selector)
	if err != nil {
		return ctrl.Result{}, err
	}
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(policy.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return ctrl.Result{}, err
	}
	matched := make([]string, 0, len(deployments.Items))
	for _, d := range deployments.Items {
		matched = append(matched, d.Name)
	}
	policy.Status.MatchedWorkloads = matched

	var token string
	if ref := policy.Spec.SonarTokenSecretRef; ref != nil {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: ref.Name}, &secret); err != nil {
			return ctrl.Result{}, err
		}
		token = string(secret.Data[sonarTokenSecretKey])
	}

	gateStatus, err := fetchGateStatus(ctx, policy.Spec.SonarServer, policy.Spec.ProjectKey, token)
	if err != nil {
		meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "SonarQubeUnreachable",
			Message: err.Error(),
		})
		if updateErr := r.Status().Update(ctx, &policy); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	now := metav1.Now()
	policy.Status.GateStatus = gateStatus
	policy.Status.LastChecked = &now
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "GateStatusFetched",
		Message: fmt.Sprintf("last SonarQube gate status: %s", gateStatus),
	})

	if err := r.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *QualityGatePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&qgatev1alpha1.QualityGatePolicy{}).
		Named("qualitygatepolicy").
		Complete(r)
}

type sonarQubeResponse struct {
	ProjectStatus struct {
		Status string `json:"status"`
	} `json:"projectStatus"`
}

func fetchGateStatus(ctx context.Context, sonarServer, projectKey, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/api/qualitygates/project_status?projectKey=%s", sonarServer, projectKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		httpReq.SetBasicAuth(token, "")
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sonarqube returned status %d", resp.StatusCode)
	}

	var body sonarQubeResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}

	return body.ProjectStatus.Status, nil
}
