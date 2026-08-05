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
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
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
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/openshift"
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
	podNamespaceEnvVar            = "POD_NAMESPACE"
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

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var oidcIssuerRefreshInterval time.Duration
	var workloadIdentityRefreshInterval time.Duration
	var azureScopeFlags azureScopeFlagValues
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	registerOIDCIssuerRefreshIntervalFlags(flag.CommandLine, &oidcIssuerRefreshInterval)
	registerAzureScopeFlags(flag.CommandLine, &azureScopeFlags)
	flag.DurationVar(
		&workloadIdentityRefreshInterval,
		"workload-identity-refresh-interval",
		controller.DefaultWorkloadIdentityRefreshInterval,
		"Base interval for successful WorkloadIdentity reconciles to revalidate Azure resources and repair "+
			"ServiceAccount drift; each resource receives up to 10% stable jitter.",
	)
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	azureScope, err := azure.NewScope(
		azureScopeFlags.subscriptionID,
		azureScopeFlags.resourceGroupName,
		azureScopeFlags.location,
	)
	if err != nil {
		setupLog.Error(err, "Invalid Azure scope configuration")
		os.Exit(1)
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServerOptions := webhook.Options{
		Port:    webhookServerPort,
		TLSOpts: tlsOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
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

	azureCredential, err := azidentity.NewDefaultAzureCredential(nil)
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
	var serviceAccountTokens controller.ServiceAccountTokenClient
	if serviceAccountTokenReader != nil {
		serviceAccountTokens = serviceAccountTokenReader
	}

	if err := (&controller.OIDCIssuerReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Publisher: &azure.BlobOIDCDocumentPublisher{
			Reader:     mgr.GetAPIReader(),
			Credential: azureCredential,
			Scope:      azureScope,
		},
		OpenShiftServiceAccountIssuer: openShiftServiceAccountIssuer,
		ServiceAccountTokens:          serviceAccountTokens,
		OIDCIssuerRefreshInterval:     oidcIssuerRefreshInterval,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "oidcissuer")
		os.Exit(1)
	}
	workloadIdentityManager := &azure.WorkloadIdentityManager{
		Credential: azureCredential,
		Scope:      azureScope,
	}
	if err := (&controller.WorkloadIdentityReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		RefreshInterval:  workloadIdentityRefreshInterval,
		Recorder:         mgr.GetEventRecorder("workloadidentity-controller"),
		Manager:          workloadIdentityManager,
		RecoveryDetector: workloadIdentityManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "workloadidentity")
		os.Exit(1)
	}
	if err := (&controller.WorkloadIdentityRecoveryReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Scheme:    mgr.GetScheme(),
		Manager: &azure.WorkloadIdentityRecoveryManager{
			Credential: azureCredential,
			Scope:      azureScope,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "workloadidentityrecovery")
		os.Exit(1)
	}
	webhooksEnabled := os.Getenv(enableWebhooksEnvVar) != "false"
	if webhooksEnabled {
		var err error
		if serviceAccountTokenReader == nil {
			err = webhookv1alpha1.SetupOIDCIssuerWebhookWithManager(mgr, webhookOpenShiftServiceAccountIssuer, nil)
		} else {
			err = webhookv1alpha1.SetupOIDCIssuerWebhookWithManager(
				mgr,
				webhookOpenShiftServiceAccountIssuer,
				serviceAccountTokenReader,
			)
		}
		if err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "OIDCIssuer")
			os.Exit(1)
		}
		if err := webhookv1alpha1.SetupWorkloadIdentityWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "WorkloadIdentity")
			os.Exit(1)
		}
		if err := webhookv1alpha1.SetupWorkloadIdentityRecoveryWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "WorkloadIdentityRecovery")
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
