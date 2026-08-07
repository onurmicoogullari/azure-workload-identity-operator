//go:build integration

package chart_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	testutil "github.com/onurmicoogullari/azure-workload-identity-operator/test/utils"
)

const (
	chartPath          = "dist/chart"
	operatorNamespace  = "azure-workload-identity-operator-system"
	webhookNamespace   = "microsoft-azure-workload-identity-webhook-system"
	operatorRelease    = "azure-workload-identity-operator"
	operatorDeployment = "azure-workload-identity-operator-controller-manager"
	credentialsSecret  = "operator-azure-credentials"
	defaultLocation    = "swedencentral"
	setString          = "--set-string"

	validatingWebhook = "azure-workload-identity-operator-validating-webhook-configuration"
	mutatingWebhook   = "azure-wi-webhook-mutating-webhook-configuration"
)

type commandRunner struct {
	root string
}

func TestHelmChartLifecycle(t *testing.T) {
	root, err := testutil.ProjectDir()
	if err != nil {
		t.Fatal(err)
	}
	runner := commandRunner{root: root}

	t.Cleanup(func() {
		if t.Failed() {
			t.Log("Preserving failed integration-test resources for CI diagnostics")
			return
		}
		for _, release := range []string{operatorRelease, "second-release", "replacement-release"} {
			runner.cleanup(t, "helm", "uninstall", release, "--namespace", operatorNamespace, "--wait", "--timeout", "5m")
		}
		runner.cleanup(t, "kubectl", "delete", "crd",
			"oidcissuers.workloadidentity.azure.micosolutions.se",
			"workloadidentities.workloadidentity.azure.micosolutions.se",
			"workloadidentityrecoveries.workloadidentity.azure.micosolutions.se",
			"--ignore-not-found", "--wait=false")
		runner.cleanup(t, "kubectl", "delete", "serviceaccount", "mutation-probe",
			"--namespace", "default", "--ignore-not-found")
		runner.cleanup(t, "kubectl", "delete", "namespace", operatorNamespace, webhookNamespace,
			"--ignore-not-found", "--wait=false")
	})

	stage(t, "server-side manifest validation", func(t *testing.T) {
		runner.run(t, "kubectl", "create", "namespace", operatorNamespace)
		templateArgs := []string{
			"template", operatorRelease, chartPath,
			"--namespace", operatorNamespace,
		}
		templateArgs = append(templateArgs, requiredValues(defaultLocation)...)
		templateArgs = append(templateArgs,
			setString, "manager.image.repository=controller",
			setString, "manager.image.tag=latest",
			"--set", "manager.image.pullPolicy=Never",
			"--set", "azureWorkloadIdentityWebhook.enabled=false",
		)
		manifest := runner.run(t, "helm", templateArgs...)
		manifestPath := writeTempFile(t, "rendered-chart.yaml", manifest)
		runner.run(t, "kubectl", "apply", "--server-side", "--dry-run=server", "-f", manifestPath)
	})

	stage(t, "install, upgrade, certificates, and RBAC", func(t *testing.T) {
		runner.run(t, "kubectl", "create", "secret", "generic", credentialsSecret,
			"--namespace", operatorNamespace,
			"--from-literal=AZURE_CLIENT_ID=00000000-0000-0000-0000-000000000001",
			"--from-literal=AZURE_TENANT_ID=00000000-0000-0000-0000-000000000000",
			"--from-literal=AZURE_CLIENT_SECRET=not-a-real-secret")

		runner.run(t, "helm", installArguments(operatorRelease, defaultLocation, true)...)
		runner.run(t, "helm", "upgrade", operatorRelease, chartPath,
			"--namespace", operatorNamespace, "--reuse-values", "--wait", "--timeout", "5m")

		if output, err := runner.result("helm", "upgrade", operatorRelease, chartPath,
			"--namespace", operatorNamespace, "--reuse-values", setString, "azure.location=westus"); err == nil {
			t.Fatalf("upgrade unexpectedly changed immutable Azure installation scope:\n%s", output)
		}

		runner.run(t, "kubectl", "wait", "certificate/azure-workload-identity-operator-serving-cert",
			"--namespace", operatorNamespace, "--for=condition=Ready", "--timeout=5m")
		runner.run(t, "kubectl", "wait", "certificate/azure-wi-webhook-serving-cert",
			"--namespace", webhookNamespace, "--for=condition=Ready", "--timeout=5m")

		eventually(t, 2*time.Minute, "operator validating webhook CA injection", func() (bool, string) {
			ca, err := runner.result("kubectl", "get", "validatingwebhookconfiguration", validatingWebhook,
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			return err == nil && strings.TrimSpace(ca) != "", ca
		})
		eventually(t, 2*time.Minute, "Azure mutating webhook CA injection", func() (bool, string) {
			ca, err := runner.result("kubectl", "get", "mutatingwebhookconfiguration", mutatingWebhook,
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			return err == nil && strings.TrimSpace(ca) != "", ca
		})

		webhookServiceAccount := "system:serviceaccount:" + webhookNamespace + ":azure-wi-webhook-admin"
		assertCanI(t, runner, "no", webhookServiceAccount, "get", "secrets", "--namespace", webhookNamespace)
		assertCanI(t, runner, "no", webhookServiceAccount, "update",
			"mutatingwebhookconfigurations.admissionregistration.k8s.io")

		operatorServiceAccount := "system:serviceaccount:" + operatorNamespace +
			":azure-workload-identity-operator-controller-manager"
		assertCanI(t, runner, "yes", operatorServiceAccount, "get", "secret/"+credentialsSecret,
			"--namespace", operatorNamespace)
		assertCanI(t, runner, "no", operatorServiceAccount, "list", "secrets", "--namespace", operatorNamespace)
		assertCanI(t, runner, "no", operatorServiceAccount, "watch", "secrets", "--namespace", operatorNamespace)
	})

	stage(t, "certificate rotation and mutating admission", func(t *testing.T) {
		certificate := "azure-wi-webhook-serving-cert"
		oldRevisionText := runner.run(t, "kubectl", "get", "certificate", certificate,
			"--namespace", webhookNamespace, "-o", "jsonpath={.status.revision}")
		oldRevision, err := strconv.Atoi(strings.TrimSpace(oldRevisionText))
		if err != nil {
			t.Fatalf("invalid initial Certificate revision %q: %v", oldRevisionText, err)
		}
		oldCA := strings.TrimSpace(runner.run(t, "kubectl", "get", "mutatingwebhookconfiguration", mutatingWebhook,
			"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}"))

		runner.run(t, "kubectl", "delete", "secret", "azure-wi-webhook-server-cert",
			"--namespace", webhookNamespace)

		eventually(t, 3*time.Minute, "Certificate revision increase", func() (bool, string) {
			output, commandErr := runner.result("kubectl", "get", "certificate", certificate,
				"--namespace", webhookNamespace, "-o", "jsonpath={.status.revision}")
			revision, parseErr := strconv.Atoi(strings.TrimSpace(output))
			return commandErr == nil && parseErr == nil && revision > oldRevision, output
		})
		eventually(t, 3*time.Minute, "rotated mutating webhook CA injection", func() (bool, string) {
			output, commandErr := runner.result("kubectl", "get", "mutatingwebhookconfiguration", mutatingWebhook,
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			ca := strings.TrimSpace(output)
			return commandErr == nil && ca != "" && ca != oldCA, output
		})

		runner.run(t, "kubectl", "create", "serviceaccount", "mutation-probe", "--namespace", "default")
		runner.run(t, "kubectl", "annotate", "serviceaccount", "mutation-probe", "--namespace", "default",
			"azure.workload.identity/client-id=00000000-0000-0000-0000-000000000001")
		probePath := writeTempFile(t, "mutation-probe.yaml", mutationProbeManifest)
		eventually(t, 2*time.Minute, "real API-server workload mutation", func() (bool, string) {
			output, commandErr := runner.result("kubectl", "apply", "--server-side", "--dry-run=server",
				"-o", "yaml", "-f", probePath)
			return commandErr == nil && strings.Contains(output, "AZURE_FEDERATED_TOKEN_FILE"), output
		})
	})

	stage(t, "validating admission fails closed", func(t *testing.T) {
		workloadIdentityPath := writeTempFile(t, "workloadidentity.yaml", workloadIdentityManifest)
		runner.run(t, "kubectl", "apply", "--server-side", "--dry-run=server", "-f", workloadIdentityPath)
		runner.run(t, "kubectl", "scale", "deployment", operatorDeployment,
			"--namespace", operatorNamespace, "--replicas=0")
		eventually(t, 2*time.Minute, "operator Pods to terminate", func() (bool, string) {
			output, commandErr := runner.result("kubectl", "get", "pods", "--namespace", operatorNamespace,
				"--selector=control-plane=controller-manager", "--output=name")
			return commandErr == nil && strings.TrimSpace(output) == "", output
		})
		if output, err := runner.result("kubectl", "apply", "--server-side", "--dry-run=server",
			"-f", workloadIdentityPath); err == nil {
			t.Fatalf("admission unexpectedly succeeded while the validating webhook was unavailable:\n%s", output)
		}
		runner.run(t, "kubectl", "scale", "deployment", operatorDeployment,
			"--namespace", operatorNamespace, "--replicas=2")
		runner.run(t, "kubectl", "rollout", "status", "deployment/"+operatorDeployment,
			"--namespace", operatorNamespace, "--timeout=5m")
		applyArgs := []string{"apply", "-f", workloadIdentityPath}
		eventually(t, 2*time.Minute, "post-recovery validating admission", func() (bool, string) {
			output, commandErr := runner.result("kubectl", applyArgs...)
			return commandErr == nil, fmt.Sprintf(
				"%s failed: %v\n%s",
				formatCommand("kubectl", applyArgs),
				commandErr,
				output,
			)
		})
	})

	stage(t, "single-release ownership and retained resources", func(t *testing.T) {
		if output, err := runner.result("helm", installArguments("second-release", defaultLocation, false)...); err == nil {
			t.Fatalf("a second cluster-wide release unexpectedly succeeded:\n%s", output)
		}

		runner.run(t, "helm", "uninstall", operatorRelease, "--namespace", operatorNamespace,
			"--wait", "--timeout", "5m")
		for _, resource := range []string{
			"crd/oidcissuers.workloadidentity.azure.micosolutions.se",
			"crd/workloadidentities.workloadidentity.azure.micosolutions.se",
			"crd/workloadidentityrecoveries.workloadidentity.azure.micosolutions.se",
			"workloadidentity/admission-probe",
			"namespace/" + webhookNamespace,
			"configmap/azure-workload-identity-operator-startup-config",
		} {
			args := []string{"get", resource}
			if strings.HasPrefix(resource, "workloadidentity/") {
				args = append(args, "--namespace", "default")
			}
			if strings.HasPrefix(resource, "configmap/") {
				args = append(args, "--namespace", operatorNamespace)
			}
			runner.run(t, "kubectl", args...)
		}
		assertAbsent(t, runner, "validatingwebhookconfiguration", validatingWebhook)
		assertAbsent(t, runner, "mutatingwebhookconfiguration", mutatingWebhook)
		assertAbsent(t, runner, "certificate", "azure-wi-webhook-serving-cert", "--namespace", webhookNamespace)

		if output, err := runner.result("helm", installArguments(operatorRelease, "westus", true)...); err == nil {
			t.Fatalf("reinstall unexpectedly changed retained Azure installation scope:\n%s", output)
		}
		runner.run(t, "helm", installArguments(operatorRelease, defaultLocation, true)...)
		runner.run(t, "kubectl", "get", "workloadidentity", "admission-probe", "--namespace", "default")
		runner.run(t, "kubectl", "rollout", "status", "deployment/"+operatorDeployment,
			"--namespace", operatorNamespace, "--timeout=5m")
		runner.run(t, "helm", "uninstall", operatorRelease, "--namespace", operatorNamespace,
			"--wait", "--timeout", "5m")
		runner.run(t, "kubectl", "get", "workloadidentity", "admission-probe", "--namespace", "default")
		runner.run(t, "kubectl", "get", "configmap", "azure-workload-identity-operator-startup-config",
			"--namespace", operatorNamespace)

		if output, err := runner.result("helm", installArguments("replacement-release", defaultLocation, false)...); err == nil {
			t.Fatalf("a different release unexpectedly adopted retained resources:\n%s", output)
		}
	})
}

func stage(t *testing.T, name string, test func(*testing.T)) {
	t.Helper()
	if !t.Run(name, test) {
		t.FailNow()
	}
}

func requiredValues(location string) []string {
	return []string{
		setString, "azure.tenantId=00000000-0000-0000-0000-000000000000",
		setString, "azure.subscriptionId=00000000-0000-0000-0000-000000000000",
		setString, "azure.resourceGroupName=rg-chart-test",
		setString, "azure.location=" + location,
	}
}

func installArguments(release, location string, includeRuntimeValues bool) []string {
	args := []string{"upgrade", "--install", release, chartPath, "--namespace", operatorNamespace}
	args = append(args, requiredValues(location)...)
	if includeRuntimeValues {
		args = append(args,
			setString, "azure.credentials.existingSecret="+credentialsSecret,
			setString, "manager.image.repository=controller",
			setString, "manager.image.tag=latest",
			"--set", "manager.image.pullPolicy=Never",
			"--wait", "--timeout", "5m",
		)
	}
	return args
}

func assertCanI(t *testing.T, runner commandRunner, expected, serviceAccount, verb, resource string, extra ...string) {
	t.Helper()
	args := []string{"auth", "can-i", verb, resource, "--as", serviceAccount}
	args = append(args, extra...)
	output, err := runner.result("kubectl", args...)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	actual := strings.TrimSpace(lines[len(lines)-1])
	if actual != expected {
		t.Fatalf(
			"kubectl auth can-i %s %s as %s = %q, want %q (error: %v; output: %q)",
			verb,
			resource,
			serviceAccount,
			actual,
			expected,
			err,
			output,
		)
	}
}

func assertAbsent(t *testing.T, runner commandRunner, resource, name string, extra ...string) {
	t.Helper()
	args := []string{"get", resource, name}
	args = append(args, extra...)
	if output, err := runner.result("kubectl", args...); err == nil {
		t.Fatalf("%s/%s unexpectedly remained:\n%s", resource, name, output)
	}
}

func eventually(
	t *testing.T,
	timeout time.Duration,
	description string,
	condition func() (bool, string),
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	lastOutput := ""
	for time.Now().Before(deadline) {
		var done bool
		done, lastOutput = condition()
		if done {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for %s; last output:\n%s", description, lastOutput)
}

func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (runner commandRunner) run(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := runner.result(name, args...)
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", formatCommand(name, args), err, output)
	}
	return output
}

func (runner commandRunner) result(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = runner.root
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (runner commandRunner) cleanup(t *testing.T, name string, args ...string) {
	t.Helper()
	if output, err := runner.result(name, args...); err != nil && !strings.Contains(output, "not found") {
		t.Logf("cleanup command %s failed: %v\n%s", formatCommand(name, args), err, output)
	}
}

func formatCommand(name string, args []string) string {
	return fmt.Sprintf("%s %s", name, strings.Join(args, " "))
}

const mutationProbeManifest = `apiVersion: v1
kind: Pod
metadata:
  name: mutation-probe
  namespace: default
  labels:
    azure.workload.identity/use: "true"
spec:
  serviceAccountName: mutation-probe
  containers:
    - name: probe
      image: registry.k8s.io/pause:3.10@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a
`

const workloadIdentityManifest = `apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: admission-probe
  namespace: default
spec:
  azure:
    userAssignedIdentityName: admission-probe
    federatedIdentityCredentialName: admission-probe
  serviceAccount:
    name: admission-probe
  deletionPolicy: Retain
`
