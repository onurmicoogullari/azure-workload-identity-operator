//go:build e2e

/*
Copyright 2026.

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

package upgrade

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	testutil "github.com/onurmicoogullari/azure-workload-identity-operator/test/utils"
)

const (
	baselineRefEnv  = "UPGRADE_E2E_BASELINE_REF"
	candidateRefEnv = "UPGRADE_E2E_CANDIDATE_REF"

	operatorRelease      = "azure-workload-identity-operator"
	operatorNamespace    = "azure-workload-identity-operator-system"
	webhookNamespace     = "microsoft-azure-workload-identity-webhook-system"
	operatorDeployment   = "azure-workload-identity-operator-controller-manager"
	webhookDeployment    = "azure-wi-webhook-controller-manager"
	validatingWebhook    = "azure-workload-identity-operator-validating-webhook-configuration"
	mutatingWebhook      = "azure-wi-webhook-mutating-webhook-configuration"
	operatorCertificate  = "azure-workload-identity-operator-serving-cert"
	webhookCertificate   = "azure-wi-webhook-serving-cert"
	credentialsSecret    = "upgrade-e2e-azure-credentials"
	certManagerVersion   = "v1.21.1"
	defaultKindCluster   = "azure-workload-identity-operator-upgrade-e2e"
	testImageRepository  = "upgrade-e2e.local/azure-workload-identity-operator"
	helmTimeout          = "8m"
	commandLogLimit      = 4000
	admissionRetryWindow = 30 * time.Second
	azureTenantID        = "00000000-0000-0000-0000-000000000000"
	azureSubscriptionID  = "00000000-0000-0000-0000-000000000000"
	azureResourceGroup   = "rg-kind-upgrade-e2e"
	azureLocation        = "swedencentral"
	workloadIdentityName = "upgrade-retained"
	workloadIdentityNS   = "default"
	oidcIssuerName       = "default"
	helmReleaseNameLabel = "meta.helm.sh/release-name"
	helmReleaseNSLabel   = "meta.helm.sh/release-namespace"
)

type commandRunner struct {
	root string
}

type revisionArtifact struct {
	commit    string
	sourceDir string
	chart     string
	image     string
	imageTag  string
	crdNames  []string
}

type resourceSnapshot struct {
	UID  string
	Spec json.RawMessage
}

type ownershipSnapshot struct {
	UID              string
	ReleaseName      string
	ReleaseNamespace string
}

type helmHistoryEntry struct {
	Revision int    `json:"revision"`
	Status   string `json:"status"`
	Chart    string `json:"chart"`
}

var executableEnvironment = map[string]string{
	"git":     "GIT",
	"helm":    "HELM",
	"kind":    "KIND",
	"kubectl": "KUBECTL",
	"tar":     "TAR",
}

func TestKindUpgradeRollback(t *testing.T) {
	root, err := testutil.ProjectDir()
	if err != nil {
		t.Fatal(err)
	}
	runner := commandRunner{root: root}
	var builtImages []string
	t.Cleanup(func() {
		removeTestImages(t, runner, builtImages)
	})
	t.Cleanup(func() {
		if t.Failed() {
			collectDiagnostics(t, runner)
		}
	})

	containerTool := os.Getenv("CONTAINER_TOOL")
	if containerTool == "" {
		containerTool = "docker"
	}
	for _, tool := range []string{containerTool, "git", "helm", "kind", "kubectl", "tar"} {
		executable := runner.executable(tool)
		if _, err := exec.LookPath(executable); err != nil {
			t.Fatalf("required tool %q is unavailable: %v", executable, err)
		}
	}
	verifyDedicatedKindContext(t, runner)

	baselineCommit := resolveCommitInput(t, runner, baselineRefEnv)
	candidateCommit := resolveCommitInput(t, runner, candidateRefEnv)
	if err := validateDistinctCommits(baselineCommit, candidateCommit); err != nil {
		t.Fatal(err)
	}
	t.Logf("Testing packaged upgrade %s -> %s", baselineCommit, candidateCommit)

	workspace := t.TempDir()
	baseline := prepareRevision(t, runner, workspace, "baseline", baselineCommit)
	candidate := prepareRevision(t, runner, workspace, "candidate", candidateCommit)
	packagedCRDNames := mergeCRDNames(baseline.crdNames, candidate.crdNames)
	if baseline.image == candidate.image || baseline.chart == candidate.chart {
		t.Fatal("baseline and candidate build/package identities must be distinct")
	}

	buildAndLoadImage(t, runner, baseline, &builtImages)
	buildAndLoadImage(t, runner, candidate, &builtImages)
	installCertManager(t, runner)
	createRuntimePrerequisites(t, runner)

	stage(t, "install packaged baseline", func(t *testing.T) {
		runner.run(t, "helm", installOrUpgradeArgs(baseline, true)...)
		assertHelmRevision(t, runner, 1)
		verifyRuntime(t, runner, baseline.image)
	})

	stage(t, "create and snapshot portable baseline state", func(t *testing.T) {
		applyRepresentativeResources(t, runner)
	})
	var baselineResources map[string]resourceSnapshot
	stage(t, "capture baseline resource snapshots", func(t *testing.T) {
		baselineResources = captureRepresentativeResources(t, runner)
	})
	var baselineCRDs map[string]crdState
	stage(t, "capture baseline CRD state", func(t *testing.T) {
		baselineCRDs = captureCRDStates(t, runner, baseline.crdNames)
	})
	var baselineOwnership map[string]ownershipSnapshot
	stage(t, "capture baseline Helm ownership", func(t *testing.T) {
		baselineOwnership = captureOwnership(t, runner, baseline.crdNames)
	})
	stage(t, "verify singleton baseline release", func(t *testing.T) {
		assertSingleOperatorRelease(t, runner)
	})

	stage(t, "upgrade to packaged candidate", func(t *testing.T) {
		runner.run(t, "helm", installOrUpgradeArgs(candidate, false)...)
		assertHelmRevision(t, runner, 2)
		verifyRuntime(t, runner, candidate.image)
		assertRepresentativeResources(t, runner, baselineResources)
		assertOwnership(t, runner, baseline.crdNames, baselineOwnership)
		assertSingleOperatorRelease(t, runner)
		verifyValidAdmissionPath(t, runner, "candidate")
		verifyWorkloadMutation(t, runner, "candidate")
	})

	candidateCRDs := captureCRDStates(t, runner, packagedCRDNames)
	compatible, reasons := rollbackCompatibility(
		baseline.crdNames, candidate.crdNames, baselineCRDs, candidateCRDs)
	if !compatible {
		t.Logf("ROLL-FORWARD-ONLY: packaged baseline rollback is disabled: %s", strings.Join(reasons, "; "))
		stage(t, "verify roll-forward-only state", func(t *testing.T) {
			assertHelmRevision(t, runner, 2)
			verifyRuntime(t, runner, candidate.image)
			assertRepresentativeResources(t, runner, baselineResources)
			assertOwnership(t, runner, baseline.crdNames, baselineOwnership)
		})
		return
	}

	stage(t, "rollback to packaged baseline", func(t *testing.T) {
		runner.run(t, "helm", "rollback", operatorRelease, "1",
			"--namespace", operatorNamespace, "--wait", "--timeout", helmTimeout)
		assertHelmRevision(t, runner, 3)
		verifyRuntime(t, runner, baseline.image)
		rolledBackCRDs := captureCRDStates(t, runner, packagedCRDNames)
		assertCRDStatesRetained(t, candidateCRDs, rolledBackCRDs)
		assertRepresentativeResources(t, runner, baselineResources)
		assertOwnership(t, runner, baseline.crdNames, baselineOwnership)
		assertSingleOperatorRelease(t, runner)
		verifyValidAdmissionPath(t, runner, "rollback")
		verifyWorkloadMutation(t, runner, "rollback")
	})
}

func stage(t *testing.T, name string, test func(*testing.T)) {
	t.Helper()
	if !t.Run(name, test) {
		t.FailNow()
	}
}

func resolveCommitInput(t *testing.T, runner commandRunner, envName string) string {
	t.Helper()
	input := strings.TrimSpace(os.Getenv(envName))
	if err := validateCommitID(envName, input); err != nil {
		t.Fatal(err)
	}
	resolved := strings.TrimSpace(runner.run(t, "git", "rev-parse", "--verify", input+"^{commit}"))
	if resolved != input {
		t.Fatalf("%s=%s resolved to unexpected commit %s", envName, input, resolved)
	}
	return resolved
}

func prepareRevision(
	t *testing.T,
	runner commandRunner,
	workspace string,
	role string,
	commit string,
) revisionArtifact {
	t.Helper()
	sourceDir := filepath.Join(workspace, role)
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(workspace, role+".tar")
	runner.run(t, "git", "archive", "--format=tar", "--output="+archive, commit)
	runner.runAt(t, sourceDir, "tar", "-xf", archive)

	chartDir := filepath.Join(sourceDir, "dist", "chart")
	if _, err := os.Stat(filepath.Join(chartDir, "Chart.yaml")); err != nil {
		t.Fatalf("commit %s does not contain the packaged Helm chart: %v", commit, err)
	}
	runner.run(t, "helm", "dependency", "build", "--skip-refresh", chartDir)
	runner.run(t, "helm", append([]string{"lint", chartDir}, azureValueArgs()...)...)
	packageDir := filepath.Join(workspace, "packages")
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	version := fmt.Sprintf("0.0.0-%s.%s", role, commit[:12])
	runner.run(t, "helm", "package", chartDir,
		"--version", version,
		"--app-version", commit,
		"--destination", packageDir)
	chart := filepath.Join(packageDir, "azure-workload-identity-operator-"+version+".tgz")
	if _, err := os.Stat(chart); err != nil {
		t.Fatalf("packaged %s chart was not created: %v", role, err)
	}
	runID := strings.TrimSpace(os.Getenv("UPGRADE_E2E_RUN_ID"))
	if runID == "" {
		runID = strconv.Itoa(os.Getpid())
	}
	imageTag := strings.Join([]string{role, commit, runID}, "-")
	crdNames := discoverPackagedCRDNames(t, runner, chart)
	t.Logf("Packaged %s CRDs: %s", role, strings.Join(crdNames, ", "))
	return revisionArtifact{
		commit:    commit,
		sourceDir: sourceDir,
		chart:     chart,
		image:     testImageRepository + ":" + imageTag,
		imageTag:  imageTag,
		crdNames:  crdNames,
	}
}

func discoverPackagedCRDNames(t *testing.T, runner commandRunner, chart string) []string {
	t.Helper()
	args := []string{"template", operatorRelease, chart,
		"--namespace", operatorNamespace,
		"--include-crds"}
	args = append(args, azureValueArgs()...)
	manifest, err := runner.result("helm", args...)
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", formatCommand(runner.executable("helm"), args), err, manifest)
	}
	names, err := crdNamesFromManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatalf("packaged chart %s rendered no CRDs", chart)
	}
	t.Logf("$ %s\nDiscovered %d packaged CRDs", formatCommand(runner.executable("helm"), args), len(names))
	return names
}

func buildAndLoadImage(
	t *testing.T,
	runner commandRunner,
	artifact revisionArtifact,
	builtImages *[]string,
) {
	t.Helper()
	containerTool := os.Getenv("CONTAINER_TOOL")
	if containerTool == "" {
		containerTool = "docker"
	}
	buildArgs := []string{
		"build",
		"--platform", "linux/" + runtime.GOARCH,
		"--build-arg", "TARGETOS=linux",
		"--build-arg", "TARGETARCH=" + runtime.GOARCH,
	}
	if strings.Contains(filepath.Base(containerTool), "podman") {
		// Podman does not re-include nested Go files with this repository's
		// Docker-specific .dockerignore negation. The build context is a
		// temporary immutable git archive, so disable only its copied filter.
		ignoreFile := filepath.Join(artifact.sourceDir, ".dockerignore")
		if err := os.Rename(ignoreFile, ignoreFile+".upgrade-e2e-disabled"); err != nil && !os.IsNotExist(err) {
			t.Fatalf("could not disable copied .dockerignore for Podman: %v", err)
		}
	}
	buildArgs = append(buildArgs, "--tag", artifact.image, artifact.sourceDir)
	runner.run(t, containerTool, buildArgs...)
	*builtImages = append(*builtImages, artifact.image)
	imageID := strings.TrimSpace(runner.run(t, containerTool, "image", "inspect", artifact.image,
		"--format={{.Id}}"))
	if imageID == "" {
		t.Fatalf("local image %s has invalid immutable image ID %q", artifact.image, imageID)
	}
	t.Logf("Built immutable test image %s with image ID %s", artifact.image, imageID)
	cluster := os.Getenv("KIND_CLUSTER")
	if cluster == "" {
		cluster = defaultKindCluster
	}
	imageArchive := filepath.Join(filepath.Dir(artifact.sourceDir), filepath.Base(artifact.sourceDir)+"-image.tar")
	runner.run(t, containerTool, "save", "--output", imageArchive, artifact.image)
	runner.run(t, "kind", "load", "image-archive", imageArchive, "--name", cluster)
}

func removeTestImages(t *testing.T, runner commandRunner, images []string) {
	t.Helper()
	containerTool := os.Getenv("CONTAINER_TOOL")
	if containerTool == "" {
		containerTool = "docker"
	}
	for _, image := range images {
		output, err := runner.result(containerTool, "image", "rm", image)
		if err != nil {
			t.Errorf("Could not remove test image %s: %v\n%s", image, err, output)
			continue
		}
		t.Logf("Removed test image %s", image)
	}
}

func installCertManager(t *testing.T, runner commandRunner) {
	t.Helper()
	runner.run(t, "helm", "repo", "add", "jetstack", "https://charts.jetstack.io", "--force-update")
	runner.run(t, "helm", "repo", "update", "jetstack")
	runner.run(t, "helm", "upgrade", "--install", "cert-manager", "jetstack/cert-manager",
		"--version", certManagerVersion,
		"--namespace", "cert-manager",
		"--create-namespace",
		"--set", "crds.enabled=true",
		"--wait", "--timeout", "5m")
}

func createRuntimePrerequisites(t *testing.T, runner commandRunner) {
	t.Helper()
	runner.run(t, "kubectl", "create", "namespace", operatorNamespace)
	runner.run(t, "kubectl", "label", "--overwrite", "namespace", operatorNamespace,
		"pod-security.kubernetes.io/enforce=restricted",
		"pod-security.kubernetes.io/audit=restricted",
		"pod-security.kubernetes.io/warn=restricted")
	runner.run(t, "kubectl", "create", "secret", "generic", credentialsSecret,
		"--namespace", operatorNamespace,
		"--from-literal=AZURE_CLIENT_ID=00000000-0000-0000-0000-000000000001",
		"--from-literal=AZURE_TENANT_ID="+azureTenantID,
		"--from-literal=AZURE_CLIENT_SECRET=upgrade-e2e-not-a-real-secret")
}

func installOrUpgradeArgs(artifact revisionArtifact, install bool) []string {
	args := []string{"upgrade"}
	if install {
		args = append(args, "--install")
	}
	args = append(args,
		operatorRelease, artifact.chart,
		"--namespace", operatorNamespace,
	)
	args = append(args, azureValueArgs()...)
	args = append(args,
		"--set-string", "azure.credentials.existingSecret="+credentialsSecret,
		"--set-string", "manager.image.repository="+testImageRepository,
		"--set-string", "manager.image.tag="+artifact.imageTag,
		"--set", "manager.image.pullPolicy=Never",
		"--wait", "--timeout", helmTimeout,
		"--history-max", "10",
	)
	return args
}

func azureValueArgs() []string {
	return []string{
		"--set-string", "azure.tenantId=" + azureTenantID,
		"--set-string", "azure.subscriptionId=" + azureSubscriptionID,
		"--set-string", "azure.resourceGroupName=" + azureResourceGroup,
		"--set-string", "azure.location=" + azureLocation,
	}
}

func verifyRuntime(t *testing.T, runner commandRunner, expectedImage string) {
	t.Helper()
	for _, deployment := range []struct {
		namespace string
		name      string
	}{
		{namespace: operatorNamespace, name: operatorDeployment},
		{namespace: webhookNamespace, name: webhookDeployment},
	} {
		runner.run(t, "kubectl", "rollout", "status", "deployment/"+deployment.name,
			"--namespace", deployment.namespace, "--timeout=5m")
	}
	for _, certificate := range []struct {
		namespace string
		name      string
	}{
		{namespace: operatorNamespace, name: operatorCertificate},
		{namespace: webhookNamespace, name: webhookCertificate},
	} {
		runner.run(t, "kubectl", "wait", "certificate/"+certificate.name,
			"--namespace", certificate.namespace, "--for=condition=Ready", "--timeout=5m")
	}
	for _, webhook := range []struct {
		kind string
		name string
	}{
		{kind: "validatingwebhookconfiguration", name: validatingWebhook},
		{kind: "mutatingwebhookconfiguration", name: mutatingWebhook},
	} {
		assertWebhookReady(t, runner, webhook.kind, webhook.name)
	}

	actualImage := strings.TrimSpace(runner.run(t, "kubectl", "get", "deployment", operatorDeployment,
		"--namespace", operatorNamespace,
		"-o", `jsonpath={.spec.template.spec.containers[?(@.name=="manager")].image}`))
	if actualImage != expectedImage {
		t.Fatalf("operator Deployment image = %q, want %q", actualImage, expectedImage)
	}
	assertRunningOperatorPods(t, runner, expectedImage)
}

func assertRunningOperatorPods(t *testing.T, runner commandRunner, expectedImage string) {
	t.Helper()
	output := runner.run(t, "kubectl", "get", "pods",
		"--namespace", operatorNamespace,
		"--selector=control-plane=controller-manager",
		"--output=json")
	var list struct {
		Items []struct {
			Metadata struct {
				DeletionTimestamp *time.Time `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				ContainerStatuses []struct {
					Name    string `json:"name"`
					Image   string `json:"image"`
					ImageID string `json:"imageID"`
					Ready   bool   `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &list); err != nil {
		t.Fatal(err)
	}
	readyPods := 0
	for _, pod := range list.Items {
		if pod.Metadata.DeletionTimestamp != nil {
			continue
		}
		for _, container := range pod.Spec.Containers {
			if container.Name == "manager" && container.Image != expectedImage {
				t.Fatalf("running operator Pod image = %q, want %q", container.Image, expectedImage)
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "manager" && status.Image != expectedImage {
				t.Fatalf("operator Pod status image = %q, want %q", status.Image, expectedImage)
			}
			if status.Name == "manager" && status.Ready && status.ImageID != "" {
				readyPods++
			}
		}
	}
	if readyPods == 0 {
		t.Fatalf("no ready operator Pods are running image %s", expectedImage)
	}
}

func assertWebhookReady(t *testing.T, runner commandRunner, kind, name string) {
	t.Helper()
	output := runner.run(t, "kubectl", "get", kind, name, "--output=json")
	var configuration struct {
		Webhooks []struct {
			Name          string `json:"name"`
			FailurePolicy string `json:"failurePolicy"`
			ClientConfig  struct {
				CABundle string `json:"caBundle"`
			} `json:"clientConfig"`
		} `json:"webhooks"`
	}
	if err := json.Unmarshal([]byte(output), &configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Webhooks) == 0 {
		t.Fatalf("%s/%s contains no webhooks", kind, name)
	}
	for _, webhook := range configuration.Webhooks {
		if webhook.ClientConfig.CABundle == "" {
			t.Fatalf("%s/%s webhook %s has no injected CA bundle", kind, name, webhook.Name)
		}
		if webhook.FailurePolicy != "Fail" {
			t.Fatalf("%s/%s webhook %s failurePolicy = %q, want Fail",
				kind, name, webhook.Name, webhook.FailurePolicy)
		}
	}
}

func captureRepresentativeResources(t *testing.T, runner commandRunner) map[string]resourceSnapshot {
	t.Helper()
	return map[string]resourceSnapshot{
		"oidcissuer/default": captureResource(t, runner, "oidcissuer", oidcIssuerName, ""),
		"workloadidentity/default/upgrade-retained": captureResource(
			t, runner, "workloadidentity", workloadIdentityName, workloadIdentityNS,
		),
	}
}

func captureResource(
	t *testing.T,
	runner commandRunner,
	kind string,
	name string,
	namespace string,
) resourceSnapshot {
	t.Helper()
	args := []string{"get", kind, name}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	}
	args = append(args, "--output=json")
	output := runner.run(t, "kubectl", args...)
	var resource struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal([]byte(output), &resource); err != nil {
		t.Fatal(err)
	}
	if resource.Metadata.UID == "" || len(resource.Spec) == 0 {
		t.Fatalf("%s/%s has incomplete snapshot data", kind, name)
	}
	return resourceSnapshot{UID: resource.Metadata.UID, Spec: canonicalJSON(resource.Spec)}
}

func assertRepresentativeResources(
	t *testing.T,
	runner commandRunner,
	want map[string]resourceSnapshot,
) {
	t.Helper()
	got := captureRepresentativeResources(t, runner)
	for name, expected := range want {
		actual, exists := got[name]
		if !exists {
			t.Fatalf("retained resource %s is missing", name)
		}
		if actual.UID != expected.UID {
			t.Fatalf("retained resource %s UID = %s, want %s", name, actual.UID, expected.UID)
		}
		if !slices.Equal(actual.Spec, expected.Spec) {
			t.Fatalf("retained resource %s spec changed\nwant: %s\n got: %s", name, expected.Spec, actual.Spec)
		}
	}
}

func captureCRDStates(t *testing.T, runner commandRunner, crdNames []string) map[string]crdState {
	t.Helper()
	states := make(map[string]crdState, len(crdNames))
	for _, name := range crdNames {
		output := runner.run(t, "kubectl", "get", "crd", name, "--output=json")
		var document struct {
			Metadata struct {
				UID         string            `json:"uid"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec   json.RawMessage `json:"spec"`
			Status struct {
				StoredVersions []string `json:"storedVersions"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(output), &document); err != nil {
			t.Fatal(err)
		}
		instancesOutput := runner.run(t, "kubectl", "get", name, "--all-namespaces", "--output=json")
		var instances struct {
			Items []struct {
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
					UID       string `json:"uid"`
				} `json:"metadata"`
				Spec json.RawMessage `json:"spec"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(instancesOutput), &instances); err != nil {
			t.Fatal(err)
		}
		objects := make(map[string]crdObjectState, len(instances.Items))
		for _, instance := range instances.Items {
			if instance.Metadata.Name == "" || instance.Metadata.UID == "" {
				t.Fatalf("CRD %s returned an object with incomplete metadata", name)
			}
			objectName := namespacedName(instance.Metadata.Namespace, instance.Metadata.Name)
			if _, exists := objects[objectName]; exists {
				t.Fatalf("CRD %s returned duplicate object %s", name, objectName)
			}
			objects[objectName] = crdObjectState{
				UID:  instance.Metadata.UID,
				Spec: canonicalJSON(instance.Spec),
			}
		}
		state := crdState{
			Name:             name,
			UID:              document.Metadata.UID,
			ReleaseName:      document.Metadata.Annotations[helmReleaseNameLabel],
			ReleaseNamespace: document.Metadata.Annotations[helmReleaseNSLabel],
			Spec:             canonicalJSON(document.Spec),
			StoredVersions:   document.Status.StoredVersions,
			Objects:          objects,
		}
		if err := validateCRDState(state); err != nil {
			t.Fatal(err)
		}
		if state.ReleaseName != operatorRelease || state.ReleaseNamespace != operatorNamespace {
			t.Fatalf("CRD %s Helm owner = %s/%s, want %s/%s", name,
				state.ReleaseNamespace, state.ReleaseName, operatorNamespace, operatorRelease)
		}
		states[name] = state
	}
	return states
}

func namespacedName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

func assertCRDStatesRetained(t *testing.T, want, got map[string]crdState) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("retained CRD count = %d, want %d", len(got), len(want))
	}
	for name, expected := range want {
		actual, exists := got[name]
		if !exists {
			t.Fatalf("retained CRD %s is missing", name)
		}
		if actual.UID != expected.UID {
			t.Fatalf("retained CRD %s UID = %s, want %s", name, actual.UID, expected.UID)
		}
		if !bytes.Equal(actual.Spec, expected.Spec) {
			t.Fatalf("retained CRD %s spec changed", name)
		}
		if !slices.Equal(actual.StoredVersions, expected.StoredVersions) {
			t.Fatalf("retained CRD %s stored versions = %v, want %v",
				name, actual.StoredVersions, expected.StoredVersions)
		}
		if objectReasons := compareCRDObjects(name, expected.Objects, actual.Objects); len(objectReasons) != 0 {
			t.Fatalf("retained CRD objects changed: %s", strings.Join(objectReasons, "; "))
		}
	}
}

func captureOwnership(t *testing.T, runner commandRunner, crdNames []string) map[string]ownershipSnapshot {
	t.Helper()
	resources := map[string][]string{
		"namespace/" + webhookNamespace: {"get", "namespace", webhookNamespace, "--output=json"},
		"configmap/" + operatorNamespace + "/azure-workload-identity-operator-startup-config": {
			"get", "configmap", "azure-workload-identity-operator-startup-config",
			"--namespace", operatorNamespace, "--output=json",
		},
	}
	for _, name := range crdNames {
		resources["crd/"+name] = []string{"get", "crd", name, "--output=json"}
	}

	result := make(map[string]ownershipSnapshot, len(resources))
	for key, args := range resources {
		output := runner.run(t, "kubectl", args...)
		var resource struct {
			Metadata struct {
				UID         string            `json:"uid"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(output), &resource); err != nil {
			t.Fatal(err)
		}
		owner := ownershipSnapshot{
			UID:              resource.Metadata.UID,
			ReleaseName:      resource.Metadata.Annotations[helmReleaseNameLabel],
			ReleaseNamespace: resource.Metadata.Annotations[helmReleaseNSLabel],
		}
		if owner.UID == "" || owner.ReleaseName != operatorRelease || owner.ReleaseNamespace != operatorNamespace {
			t.Fatalf("resource %s has invalid singleton ownership: %+v", key, owner)
		}
		result[key] = owner
	}
	return result
}

func assertOwnership(
	t *testing.T,
	runner commandRunner,
	crdNames []string,
	want map[string]ownershipSnapshot,
) {
	t.Helper()
	got := captureOwnership(t, runner, crdNames)
	for name, expected := range want {
		if actual := got[name]; actual != expected {
			t.Fatalf("singleton ownership changed for %s: got %+v, want %+v", name, actual, expected)
		}
	}
}

func assertSingleOperatorRelease(t *testing.T, runner commandRunner) {
	t.Helper()
	output := runner.run(t, "kubectl", "get", "secrets", "--all-namespaces",
		"--selector=owner=helm,name="+operatorRelease, "--output=json")
	var releases struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &releases); err != nil {
		t.Fatal(err)
	}
	if len(releases.Items) == 0 {
		t.Fatalf("found no Helm release revisions for %s/%s", operatorNamespace, operatorRelease)
	}
	for _, release := range releases.Items {
		if release.Metadata.Namespace != operatorNamespace {
			t.Fatalf("operator Helm release revision %s exists in unexpected namespace %s",
				release.Metadata.Name, release.Metadata.Namespace)
		}
	}
}

func assertHelmRevision(t *testing.T, runner commandRunner, want int) {
	t.Helper()
	output := runner.run(t, "helm", "history", operatorRelease,
		"--namespace", operatorNamespace, "--output=json")
	var history []helmHistoryEntry
	if err := json.Unmarshal([]byte(output), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 {
		t.Fatal("Helm history is empty")
	}
	last := history[len(history)-1]
	if last.Revision != want || last.Status != "deployed" {
		t.Fatalf("latest Helm history = revision %d status %q, want revision %d deployed\n%s",
			last.Revision, last.Status, want, output)
	}
}

func verifyValidAdmissionPath(t *testing.T, runner commandRunner, phase string) {
	t.Helper()
	manifest := fmt.Sprintf(workloadIdentityAdmissionProbe, phase, phase, phase, phase)
	output := runner.runInput(t, manifest, "kubectl", "apply", "--server-side", "--dry-run=server",
		"--output=json", "-f", "-")
	if !strings.Contains(output, `"kind": "WorkloadIdentity"`) &&
		!strings.Contains(output, `"kind":"WorkloadIdentity"`) {
		t.Fatalf("valid admission path probe returned unexpected object:\n%s", output)
	}
}

func applyRepresentativeResources(t *testing.T, runner commandRunner) {
	t.Helper()
	args := []string{"apply", "-f", "-"}
	deadline := time.Now().Add(admissionRetryWindow)
	retried := false
	for {
		output, err := runner.resultInput(representativeResources, "kubectl", args...)
		if err == nil {
			t.Logf("$ %s\n%s", formatCommand(runner.executable("kubectl"), args), commandLogOutput(output))
			return
		}
		if !strings.Contains(output, "failed calling webhook") || time.Now().After(deadline) {
			t.Fatalf("%s failed: %v\n%s", formatCommand(runner.executable("kubectl"), args), err, output)
		}
		if !retried {
			t.Log("Admission Service is not accepting connections yet; retrying for up to 30 seconds")
			retried = true
		}
		time.Sleep(time.Second)
	}
}

func verifyWorkloadMutation(t *testing.T, runner commandRunner, phase string) {
	t.Helper()
	if _, err := runner.result("kubectl", "get", "serviceaccount", "mutation-probe",
		"--namespace", workloadIdentityNS); err != nil {
		runner.run(t, "kubectl", "create", "serviceaccount", "mutation-probe",
			"--namespace", workloadIdentityNS)
	}
	runner.run(t, "kubectl", "annotate", "serviceaccount", "mutation-probe",
		"--namespace", workloadIdentityNS, "--overwrite",
		"azure.workload.identity/client-id=00000000-0000-0000-0000-000000000001")
	manifest := fmt.Sprintf(mutationProbe, phase)
	output := runner.runInput(t, manifest, "kubectl", "apply", "--server-side", "--dry-run=server",
		"--output=json", "-f", "-")
	for _, expected := range []string{"AZURE_FEDERATED_TOKEN_FILE", "azure-identity-token"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("bundled mutation probe is missing %q:\n%s", expected, output)
		}
	}
}

func verifyDedicatedKindContext(t *testing.T, runner commandRunner) {
	t.Helper()
	cluster := os.Getenv("KIND_CLUSTER")
	if cluster == "" {
		cluster = defaultKindCluster
	}
	currentContext := strings.TrimSpace(runner.run(t, "kubectl", "config", "current-context"))
	wantContext := "kind-" + cluster
	if currentContext != wantContext {
		t.Fatalf("current Kubernetes context = %q, want dedicated context %q", currentContext, wantContext)
	}
}

func collectDiagnostics(t *testing.T, runner commandRunner) {
	t.Helper()
	t.Log("Collecting Kind upgrade E2E diagnostics")
	commands := [][]string{
		{"helm", "status", operatorRelease, "--namespace", operatorNamespace},
		{"helm", "history", operatorRelease, "--namespace", operatorNamespace},
		{"kubectl", "get", "all", "--all-namespaces", "--output=wide"},
		{"kubectl", "get", "crd", "--output=yaml"},
		{"kubectl", "get", "oidcissuer,workloadidentity,workloadidentityrecovery", "--all-namespaces", "--output=yaml"},
		{"kubectl", "get", "validatingwebhookconfigurations,mutatingwebhookconfigurations", "--output=yaml"},
		{"kubectl", "get", "certificates,issuers", "--all-namespaces", "--output=wide"},
		{"kubectl", "get", "events", "--all-namespaces", "--sort-by=.lastTimestamp"},
		{"kubectl", "logs", "deployment/" + operatorDeployment, "--namespace", operatorNamespace,
			"--all-pods=true", "--all-containers=true", "--tail=500"},
		{"kubectl", "logs", "deployment/" + webhookDeployment, "--namespace", webhookNamespace,
			"--all-pods=true", "--all-containers=true", "--tail=500"},
	}
	for _, command := range commands {
		output, err := runner.result(command[0], command[1:]...)
		t.Logf("$ %s\nerror: %v\n%s", strings.Join(command, " "), err, commandLogOutput(output))
	}
}

func (runner commandRunner) run(t *testing.T, name string, args ...string) string {
	t.Helper()
	executable := runner.executable(name)
	output, err := runner.result(name, args...)
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", formatCommand(executable, args), err, output)
	}
	t.Logf("$ %s\n%s", formatCommand(executable, args), commandLogOutput(output))
	return output
}

func (runner commandRunner) runAt(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	executable := runner.executable(name)
	cmd := exec.Command(executable, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed in %s: %v\n%s", formatCommand(executable, args), dir, err, output)
	}
	t.Logf("$ (cd %s && %s)\n%s", dir, formatCommand(executable, args), commandLogOutput(string(output)))
	return string(output)
}

func (runner commandRunner) runInput(t *testing.T, input string, name string, args ...string) string {
	t.Helper()
	executable := runner.executable(name)
	output, err := runner.resultInput(input, name, args...)
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", formatCommand(executable, args), err, output)
	}
	t.Logf("$ %s\n%s", formatCommand(executable, args), commandLogOutput(output))
	return output
}

func (runner commandRunner) resultInput(input string, name string, args ...string) (string, error) {
	executable := runner.executable(name)
	cmd := exec.Command(executable, args...)
	cmd.Dir = runner.root
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (runner commandRunner) result(name string, args ...string) (string, error) {
	cmd := exec.Command(runner.executable(name), args...)
	cmd.Dir = runner.root
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (runner commandRunner) executable(name string) string {
	environmentName, exists := executableEnvironment[name]
	if !exists {
		return name
	}
	if configured := strings.TrimSpace(os.Getenv(environmentName)); configured != "" {
		return configured
	}
	return name
}

func formatCommand(name string, args []string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

func commandLogOutput(output string) string {
	if len(output) <= commandLogLimit {
		return output
	}
	headLength := commandLogLimit / 2
	tailLength := commandLogLimit - headLength
	return output[:headLength] +
		fmt.Sprintf("\n... output truncated (%d bytes total; showing first and last %d bytes) ...\n", len(output), commandLogLimit) +
		output[len(output)-tailLength:]
}

const representativeResources = `apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: OIDCIssuer
metadata:
  name: default
spec:
  azure:
    storageAccountName: kindupgradee2e
    blobContainerName: upgrade-oidc
  signingKey:
    secretRef:
      name: upgrade-e2e-signing-key
      namespace: default
      key: public.pem
  deletionPolicy: Retain
---
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: upgrade-retained
  namespace: default
spec:
  azure:
    userAssignedIdentityName: upgrade-retained
    federatedIdentityCredentialName: upgrade-retained
  serviceAccount:
    name: upgrade-retained
  deletionPolicy: Retain
`

const workloadIdentityAdmissionProbe = `apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: %s-admission-probe
  namespace: default
spec:
  azure:
    userAssignedIdentityName: %s-admission-probe
    federatedIdentityCredentialName: %s-admission-probe
  serviceAccount:
    name: %s-admission-probe
  deletionPolicy: Retain
`

const mutationProbe = `apiVersion: v1
kind: Pod
metadata:
  name: %s-mutation-probe
  namespace: default
  labels:
    azure.workload.identity/use: "true"
spec:
  serviceAccountName: mutation-probe
  containers:
    - name: probe
      image: registry.k8s.io/pause:3.10@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a
`
