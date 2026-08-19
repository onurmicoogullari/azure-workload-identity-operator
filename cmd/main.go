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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	aztracing "github.com/Azure/azure-sdk-for-go/sdk/azcore/tracing"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/azure"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/controller"
	kubernetesclient "github.com/onurmicoogullari/azure-workload-identity-operator/internal/kubernetes"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/oidcissuer"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/openshift"
	operatortelemetry "github.com/onurmicoogullari/azure-workload-identity-operator/internal/telemetry"
	webhookv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/internal/webhook/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

const (
	enableWebhooksEnvVar          = "ENABLE_WEBHOOKS"
	operatorVersionEnvVar         = "OPERATOR_VERSION"
	podNameEnvVar                 = "POD_NAME"
	podNamespaceEnvVar            = "POD_NAMESPACE"
	podUIDEnvVar                  = "POD_UID"
	serviceAccountNameEnvVar      = "SERVICE_ACCOUNT_NAME"
	serviceAccountTokenExpiration = int64(600)
	webhookServerPort             = 9443
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(workloadidentityv1alpha1.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func registerOIDCIssuerRefreshIntervalFlags(flags *flag.FlagSet, interval *time.Duration) {
	flags.DurationVar(interval, "oidc-issuer-refresh-interval", controller.DefaultOIDCIssuerRefreshInterval,
		"How often to reconcile OIDCIssuer publishing, including signing keys, Azure storage resources, and OIDC documents.")
}

type azureScopeFlagValues struct {
	subscriptionID    string
	resourceGroupName string
	location          string
}

type certificateFlagValues struct {
	directory string
	name      string
	key       string
}

type operatorFlagValues struct {
	metricsAddress                  string
	healthProbeAddress              string
	enableLeaderElection            bool
	secureMetrics                   bool
	enableHTTP2                     bool
	telemetryTracingEnabled         bool
	oidcIssuerRefreshInterval       time.Duration
	workloadIdentityRefreshInterval time.Duration
	azureScope                      azureScopeFlagValues
	azureScopeAnchorDirectory       string
	webhookCertificate              certificateFlagValues
	metricsCertificate              certificateFlagValues
}

type healthCheckRegistrar interface {
	AddHealthzCheck(name string, check healthz.Checker) error
	AddReadyzCheck(name string, check healthz.Checker) error
}

func registerHealthChecks(registrar healthCheckRegistrar, webhookStarted healthz.Checker) error {
	if err := registrar.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add health check: %w", err)
	}
	if err := registrar.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add ready check: %w", err)
	}
	if webhookStarted != nil {
		if err := registrar.AddReadyzCheck("webhook", webhookStarted); err != nil {
			return fmt.Errorf("add webhook ready check: %w", err)
		}
	}
	return nil
}

func registerAzureScopeFlags(flags *flag.FlagSet, values *azureScopeFlagValues) {
	flags.StringVar(
		&values.subscriptionID,
		"azure-subscription-id",
		"",
		"Required Azure subscription ID for all platform-owned resources.",
	)
	flags.StringVar(
		&values.resourceGroupName,
		"azure-resource-group-name",
		"",
		"Required shared Azure resource group for OIDC storage and user assigned managed identities.",
	)
	flags.StringVar(
		&values.location,
		"azure-location",
		"",
		"Required Azure location used when creating platform-owned resources.",
	)
}

func registerOperatorFlags(flags *flag.FlagSet, values *operatorFlagValues) {
	flags.StringVar(&values.metricsAddress, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flags.StringVar(
		&values.healthProbeAddress,
		"health-probe-bind-address",
		":8081",
		"The address the probe endpoint binds to.",
	)
	flags.BoolVar(&values.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flags.BoolVar(&values.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flags.StringVar(
		&values.webhookCertificate.directory,
		"webhook-cert-path",
		"",
		"The directory that contains the webhook certificate.",
	)
	flags.StringVar(
		&values.webhookCertificate.name,
		"webhook-cert-name",
		"tls.crt",
		"The name of the webhook certificate file.",
	)
	flags.StringVar(&values.webhookCertificate.key, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flags.StringVar(&values.metricsCertificate.directory, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flags.StringVar(
		&values.metricsCertificate.name,
		"metrics-cert-name",
		"tls.crt",
		"The name of the metrics server certificate file.",
	)
	flags.StringVar(
		&values.metricsCertificate.key,
		"metrics-cert-key",
		"tls.key",
		"The name of the metrics server key file.",
	)
	flags.BoolVar(&values.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flags.BoolVar(&values.telemetryTracingEnabled, "telemetry-tracing-enabled", false,
		"Enable in-process OpenTelemetry tracing configured through standard OTEL environment variables.")
	registerOIDCIssuerRefreshIntervalFlags(flags, &values.oidcIssuerRefreshInterval)
	registerAzureScopeFlags(flags, &values.azureScope)
	flags.StringVar(
		&values.azureScopeAnchorDirectory,
		"azure-scope-anchor-directory",
		"",
		"Directory containing the retained Azure startup scope. "+
			"When set, startup fails unless it matches the configured scope.",
	)
	flags.DurationVar(
		&values.workloadIdentityRefreshInterval,
		"workload-identity-refresh-interval",
		controller.DefaultWorkloadIdentityRefreshInterval,
		"Base interval for successful WorkloadIdentity reconciles to revalidate Azure resources and repair "+
			"ServiceAccount drift; each resource receives up to 10% stable jitter.",
	)
}

func main() {
	var config operatorFlagValues
	registerOperatorFlags(flag.CommandLine, &config)
	opts := zap.Options{
		Development: false,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	telemetryRuntime := newTelemetryRuntime(config.telemetryTracingEnabled)
	defer shutdownTelemetry(telemetryRuntime)

	azureScope, err := azure.NewScope(
		config.azureScope.subscriptionID,
		config.azureScope.resourceGroupName,
		config.azureScope.location,
	)
	if err != nil {
		setupLog.Error(err, "Invalid Azure scope configuration")
		os.Exit(1)
	}
	if config.azureScopeAnchorDirectory != "" {
		if err := azure.ValidateScopeAnchor(azureScope, config.azureScopeAnchorDirectory); err != nil {
			setupLog.Error(err, "Azure startup scope validation failed")
			os.Exit(1)
		}
	}

	tlsOpts := tlsOptions(config.enableHTTP2)
	webhookServerOptions := newWebhookServerOptions(config.webhookCertificate, tlsOpts)
	webhookServer := telemetryRuntime.WrapWebhookServer(webhook.NewServer(webhookServerOptions))
	metricsServerOptions := newMetricsServerOptions(config, tlsOpts)

	restConfig := ctrl.GetConfigOrDie()
	telemetryRuntime.WrapRESTConfig(restConfig)
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: config.healthProbeAddress,
		LeaderElection:         config.enableLeaderElection,
		LeaderElectionID:       "052b777d.micosolutions.se",
		// The process exits as soon as the manager stops, so releasing the lease on
		// shutdown is safe and avoids waiting for the lease to expire during rollouts.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}
	if err := workloadidentity.IndexRecoveriesByPreviousWorkloadIdentityUID(
		context.Background(),
		mgr.GetFieldIndexer(),
	); err != nil {
		setupLog.Error(err, "Failed to configure WorkloadIdentityRecovery index")
		os.Exit(1)
	}

	azureTracingProvider := telemetryRuntime.AzureTracingProvider()
	azureCredential, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
		ClientOptions: azcore.ClientOptions{TracingProvider: azureTracingProvider},
	})
	if err != nil {
		setupLog.Error(err, "Failed to create Azure credential")
		os.Exit(1)
	}

	openShiftServiceAccountIssuer, webhookOpenShiftServiceAccountIssuer, err := openShiftServiceAccountIssuerClients(mgr)
	if err != nil {
		setupLog.Error(err, "Failed to discover OpenShift Authentication API")
		os.Exit(1)
	}
	serviceAccountTokenReader := newServiceAccountTokenClient(mgr.GetClient())
	serviceAccountTokens := serviceAccountTokenGuard(serviceAccountTokenReader)
	if name, err := registerControllers(
		mgr,
		azureCredential,
		azureScope,
		azureTracingProvider,
		openShiftServiceAccountIssuer,
		serviceAccountTokens,
		config,
		telemetryRuntime,
	); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", name)
		os.Exit(1)
	}
	webhooksEnabled := os.Getenv(enableWebhooksEnvVar) != "false"
	if webhooksEnabled {
		if name, err := registerWebhooks(
			mgr,
			webhookOpenShiftServiceAccountIssuer,
			serviceAccountTokens,
		); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", name)
			os.Exit(1)
		}
	}
	// +kubebuilder:scaffold:builder

	var webhookStarted healthz.Checker
	if webhooksEnabled {
		webhookStarted = mgr.GetWebhookServer().StartedChecker()
	}
	if err := registerHealthChecks(mgr, webhookStarted); err != nil {
		setupLog.Error(err, "Failed to set up health checks")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

func newTelemetryRuntime(enabled bool) *operatortelemetry.Runtime {
	telemetryContext := logr.NewContext(context.Background(), setupLog.WithName("telemetry"))
	telemetryRuntime, err := operatortelemetry.New(telemetryContext, operatortelemetry.Config{
		Enabled:        enabled,
		ServiceVersion: operatorVersion(),
		PodName:        os.Getenv(podNameEnvVar),
		PodUID:         os.Getenv(podUIDEnvVar),
		PodNamespace:   os.Getenv(podNamespaceEnvVar),
	})
	if err != nil {
		setupLog.Error(err, "OpenTelemetry tracing is disabled because configuration is invalid")
	}
	return telemetryRuntime
}

func shutdownTelemetry(telemetryRuntime *operatortelemetry.Runtime) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := telemetryRuntime.Shutdown(ctx); err != nil {
		setupLog.Error(err, "Could not flush OpenTelemetry spans during shutdown")
	}
}

// Disabling HTTP/2 avoids the Stream Cancellation and Rapid Reset attack
// classes while the operator does not require HTTP/2.
func tlsOptions(enableHTTP2 bool) []func(*tls.Config) {
	if enableHTTP2 {
		return nil
	}
	return []func(*tls.Config){disableHTTP2}
}

func disableHTTP2(config *tls.Config) {
	setupLog.Info("Disabling HTTP/2")
	config.NextProtos = []string{"http/1.1"}
}

func newWebhookServerOptions(
	certificate certificateFlagValues,
	tlsOpts []func(*tls.Config),
) webhook.Options {
	options := webhook.Options{Port: webhookServerPort, TLSOpts: tlsOpts}
	if certificate.directory == "" {
		return options
	}
	setupLog.Info(
		"Initializing webhook certificate watcher using provided certificates",
		"webhook-cert-path", certificate.directory,
		"webhook-cert-name", certificate.name,
		"webhook-cert-key", certificate.key,
	)
	options.CertDir = certificate.directory
	options.CertName = certificate.name
	options.KeyName = certificate.key
	return options
}

func newMetricsServerOptions(
	config operatorFlagValues,
	tlsOpts []func(*tls.Config),
) metricsserver.Options {
	options := metricsserver.Options{
		BindAddress:   config.metricsAddress,
		SecureServing: config.secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if config.secureMetrics {
		options.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	certificate := config.metricsCertificate
	if certificate.directory == "" {
		return options
	}
	setupLog.Info(
		"Initializing metrics certificate watcher using provided certificates",
		"metrics-cert-path", certificate.directory,
		"metrics-cert-name", certificate.name,
		"metrics-cert-key", certificate.key,
	)
	options.CertDir = certificate.directory
	options.CertName = certificate.name
	options.KeyName = certificate.key
	return options
}

func serviceAccountTokenGuard(
	reader *kubernetesclient.ServiceAccountTokenClient,
) controller.ServiceAccountTokenClient {
	if reader == nil {
		return nil
	}
	return reader
}

func registerControllers(
	mgr ctrl.Manager,
	credential azcore.TokenCredential,
	scope azure.Scope,
	tracingProvider aztracing.Provider,
	openShiftServiceAccountIssuer controller.OpenShiftServiceAccountIssuerManager,
	serviceAccountTokens controller.ServiceAccountTokenClient,
	config operatorFlagValues,
	telemetryRuntime *operatortelemetry.Runtime,
) (string, error) {
	if err := (&controller.OIDCIssuerReconciler{
		Client: mgr.GetClient(),
		Publisher: &azure.BlobOIDCDocumentPublisher{
			Reader:          mgr.GetAPIReader(),
			Credential:      credential,
			Scope:           scope,
			TracingProvider: tracingProvider,
		},
		OpenShiftServiceAccountIssuer: openShiftServiceAccountIssuer,
		ServiceAccountTokens:          serviceAccountTokens,
		OIDCIssuerRefreshInterval:     config.oidcIssuerRefreshInterval,
		Telemetry:                     telemetryRuntime,
	}).SetupWithManager(mgr); err != nil {
		return "oidcissuer", err
	}

	workloadIdentityManager := &azure.WorkloadIdentityManager{
		Credential:      credential,
		Scope:           scope,
		TracingProvider: tracingProvider,
	}
	if err := (&controller.WorkloadIdentityReconciler{
		Client:           mgr.GetClient(),
		RefreshInterval:  config.workloadIdentityRefreshInterval,
		Recorder:         mgr.GetEventRecorder("workloadidentity-controller"),
		Manager:          workloadIdentityManager,
		RecoveryDetector: workloadIdentityManager,
		Telemetry:        telemetryRuntime,
	}).SetupWithManager(mgr); err != nil {
		return "workloadidentity", err
	}

	err := (&controller.WorkloadIdentityRecoveryReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Manager: &azure.WorkloadIdentityRecoveryManager{
			Credential:      credential,
			Scope:           scope,
			TracingProvider: tracingProvider,
		},
		Telemetry: telemetryRuntime,
	}).SetupWithManager(mgr)
	return "workloadidentityrecovery", err
}

func registerWebhooks(
	mgr ctrl.Manager,
	openShiftServiceAccountIssuer oidcissuer.OpenShiftServiceAccountIssuerReader,
	serviceAccountTokens oidcissuer.ServiceAccountTokenIssuerReader,
) (string, error) {
	if err := webhookv1alpha1.SetupOIDCIssuerWebhookWithManager(
		mgr,
		openShiftServiceAccountIssuer,
		serviceAccountTokens,
	); err != nil {
		return "OIDCIssuer", err
	}
	if err := webhookv1alpha1.SetupWorkloadIdentityWebhookWithManager(mgr); err != nil {
		return "WorkloadIdentity", err
	}
	err := webhookv1alpha1.SetupWorkloadIdentityRecoveryWebhookWithManager(mgr)
	return "WorkloadIdentityRecovery", err
}

func operatorVersion() string {
	if version := strings.TrimSpace(os.Getenv(operatorVersionEnvVar)); version != "" {
		return strings.TrimPrefix(version, "v")
	}
	buildInfo, ok := debug.ReadBuildInfo()
	if ok && buildInfo.Main.Version != "" && buildInfo.Main.Version != "(devel)" {
		return strings.TrimPrefix(buildInfo.Main.Version, "v")
	}
	return "devel"
}

func openShiftServiceAccountIssuerClients(
	mgr ctrl.Manager,
) (*openshift.ServiceAccountIssuerClient, *openshift.ServiceAccountIssuerReader, error) {
	available, err := openshift.AuthenticationAPIAvailable(mgr.GetRESTMapper())
	if err != nil {
		return nil, nil, err
	}
	if !available {
		setupLog.Info("OpenShift Authentication API not found; skipping OpenShift service account issuer integration")
		return nil, nil, nil
	}

	setupLog.Info("OpenShift Authentication API found; enabling OpenShift service account issuer integration")
	return &openshift.ServiceAccountIssuerClient{Client: mgr.GetClient()},
		&openshift.ServiceAccountIssuerReader{Reader: mgr.GetAPIReader()},
		nil
}

func newServiceAccountTokenClient(kubeClient client.Client) *kubernetesclient.ServiceAccountTokenClient {
	namespace := os.Getenv(podNamespaceEnvVar)
	name := os.Getenv(serviceAccountNameEnvVar)
	if namespace == "" || name == "" {
		setupLog.Info("Pod service account identity not found; skipping cluster service account issuer deletion guard",
			"namespaceEnvVar", podNamespaceEnvVar,
			"serviceAccountNameEnvVar", serviceAccountNameEnvVar)
		return nil
	}

	return &kubernetesclient.ServiceAccountTokenClient{
		Client:             kubeClient,
		Namespace:          namespace,
		ServiceAccountName: name,
		ExpirationSeconds:  serviceAccountTokenExpiration,
	}
}
