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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/metin-karaca/kube-qgate-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "kube-qgate-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "kube-qgate-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "kube-qgate-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "kube-qgate-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		// The NetworkPolicy in config/network-policy only allows ingress to the metrics
		// port from Pods running in namespaces labeled "metrics: enabled" (see
		// config/network-policy/allow-metrics-traffic.yaml). The curl-metrics pod created
		// below runs in this same namespace, so it needs the label too.
		By("labeling the namespace to allow ingress to the metrics endpoint")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace, "metrics=enabled")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with metrics=enabled")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("cleaning up the metrics ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=kube-qgate-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		It("should exclude its own namespace and kube-system from the webhook", func() {
			By("reading the ValidatingWebhookConfiguration's namespaceSelector")
			cmd := exec.Command("kubectl", "get", "validatingwebhookconfiguration",
				"kube-qgate-operator-validating-webhook-configuration",
				"-o", "jsonpath={.webhooks[0].namespaceSelector.matchExpressions[0]}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "ValidatingWebhookConfiguration should exist")

			// With failurePolicy=Fail, gating the operator's own namespace would let a crashed
			// operator reject the Deployment update needed to bring it back up.
			Expect(output).To(ContainSubstring(`"operator":"NotIn"`))
			Expect(output).To(ContainSubstring(namespace))
			Expect(output).To(ContainSubstring("kube-system"))
		})

		It("should report a bounded webhook timeout", func() {
			cmd := exec.Command("kubectl", "get", "validatingwebhookconfiguration",
				"kube-qgate-operator-validating-webhook-configuration",
				"-o", "jsonpath={.webhooks[0].timeoutSeconds}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("5"))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("QualityGatePolicy", Ordered, func() {
		const testNamespace = "default"
		mockDeployments := []string{"mock-sonar-ok", "mock-sonar-error"}

		BeforeAll(func() {
			By("deploying mock SonarQube servers returning fixed OK/ERROR gate statuses")
			responses := map[string]string{
				"mock-sonar-ok":    `{"projectStatus":{"status":"OK"}}`,
				"mock-sonar-error": `{"projectStatus":{"status":"ERROR"}}`,
			}
			for _, name := range mockDeployments {
				// A plain "kubectl create deployment -- args" replaces the image's
				// entrypoint (command) rather than appending args to it, which breaks
				// http-echo (it tries to exec "-listen=:5678" as a binary). Author the
				// manifest directly so only "args" is set and the image's own
				// ENTRYPOINT is preserved.
				manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    app: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[1]s
  template:
    metadata:
      labels:
        app: %[1]s
    spec:
      containers:
        - name: http-echo
          image: hashicorp/http-echo:1.0
          args:
            - -listen=:5678
            - -text=%[3]s
          ports:
            - containerPort: 5678
---
apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  selector:
    app: %[1]s
  ports:
    - port: 5678
      targetPort: 5678
`, name, testNamespace, responses[name])
				Expect(applyManifest(manifest)).To(Succeed(), "Failed to create mock SonarQube deployment "+name)

				cmd := exec.Command("kubectl", "-n", testNamespace, "wait",
					"--for=condition=Available", "--timeout=60s", "deployment/"+name)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Mock SonarQube deployment "+name+" did not become available")
			}
		})

		AfterAll(func() {
			for _, name := range mockDeployments {
				_, _ = utils.Run(exec.Command("kubectl", "-n", testNamespace, "delete", "deployment", name, "--ignore-not-found"))
				_, _ = utils.Run(exec.Command("kubectl", "-n", testNamespace, "delete", "service", name, "--ignore-not-found"))
			}
		})

		It("reconciles an Audit-mode policy and records GateStatus OK", func() {
			policyYAML := fmt.Sprintf(`apiVersion: qgate.qgate.io/v1alpha1
kind: QualityGatePolicy
metadata:
  name: e2e-audit-policy
  namespace: %s
spec:
  selector:
    matchLabels:
      app: e2e-audit-app
  sonarServer: http://mock-sonar-ok.%s.svc.cluster.local:5678
  projectKey: e2e-audit-app
  mode: Audit
`, testNamespace, testNamespace)
			Expect(applyManifest(policyYAML)).To(Succeed())
			defer deleteManifest(policyYAML)

			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "-n", testNamespace, "get", "qualitygatepolicy",
					"e2e-audit-policy", "-o", "jsonpath={.status.gateStatus}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("OK"))
			}).Should(Succeed())
		})

		It("denies a Deployment via a Block-mode policy when the gate is ERROR", func() {
			policyYAML := fmt.Sprintf(`apiVersion: qgate.qgate.io/v1alpha1
kind: QualityGatePolicy
metadata:
  name: e2e-block-policy
  namespace: %s
spec:
  selector:
    matchLabels:
      app: e2e-blocked-app
  sonarServer: http://mock-sonar-error.%s.svc.cluster.local:5678
  projectKey: e2e-blocked-app
  mode: Block
`, testNamespace, testNamespace)
			Expect(applyManifest(policyYAML)).To(Succeed())
			defer deleteManifest(policyYAML)

			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "-n", testNamespace, "get", "qualitygatepolicy",
					"e2e-block-policy", "-o", "jsonpath={.status.gateStatus}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("ERROR"))
			}).Should(Succeed())

			deploymentYAML := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: e2e-blocked-app
  namespace: %s
  labels:
    app: e2e-blocked-app
spec:
  replicas: 1
  selector:
    matchLabels:
      app: e2e-blocked-app
  template:
    metadata:
      labels:
        app: e2e-blocked-app
    spec:
      containers:
      - name: app
        image: nginx:latest
`, testNamespace)

			By("verifying the admission webhook rejects the Deployment")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deploymentYAML)
			output, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred())
			Expect(output).To(ContainSubstring("blocks this Deployment"))
		})

		It("denies a Deployment once an OK gate status has gone stale", func() {
			// The controller polls every 5 minutes, so a 5-second maxStaleness makes the freshly
			// fetched OK untrustworthy almost immediately — the same situation a real cluster is in
			// when SonarQube becomes unreachable and the cached OK ages out.
			policyYAML := fmt.Sprintf(`apiVersion: qgate.qgate.io/v1alpha1
kind: QualityGatePolicy
metadata:
  name: e2e-stale-policy
  namespace: %s
spec:
  selector:
    matchLabels:
      app: e2e-stale-app
  sonarServer: http://mock-sonar-ok.%s.svc.cluster.local:5678
  projectKey: e2e-stale-app
  mode: Block
  maxStaleness: 5s
`, testNamespace, testNamespace)
			Expect(applyManifest(policyYAML)).To(Succeed())
			defer deleteManifest(policyYAML)

			By("waiting for the controller to record an OK gate status")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "-n", testNamespace, "get", "qualitygatepolicy",
					"e2e-stale-policy", "-o", "jsonpath={.status.gateStatus}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("OK"))
			}).Should(Succeed())

			deploymentYAML := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: e2e-stale-app
  namespace: %s
  labels:
    app: e2e-stale-app
spec:
  replicas: 1
  selector:
    matchLabels:
      app: e2e-stale-app
  template:
    metadata:
      labels:
        app: e2e-stale-app
    spec:
      containers:
      - name: app
        image: nginx:1.27
`, testNamespace)

			By("verifying the webhook stops trusting the OK once it ages past maxStaleness")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "apply", "--dry-run=server", "-f", "-")
				cmd.Stdin = strings.NewReader(deploymentYAML)
				out, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred())
				g.Expect(out).To(ContainSubstring("exceeding maxStaleness"))
			}).Should(Succeed())
		})
	})
})

// applyManifest applies the given YAML manifest via "kubectl apply -f -".
func applyManifest(yaml string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	_, err := utils.Run(cmd)
	return err
}

// deleteManifest deletes the given YAML manifest via "kubectl delete -f -", ignoring not-found errors.
func deleteManifest(yaml string) {
	cmd := exec.Command("kubectl", "delete", "-f", "-", "--ignore-not-found")
	cmd.Stdin = strings.NewReader(yaml)
	_, _ = utils.Run(cmd)
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
