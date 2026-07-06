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

package controller

import (
	"context"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/oidc"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

type countingOIDCPublisher struct {
	publishes atomic.Int32
}

func (p *countingOIDCPublisher) Publish(context.Context, *workloadidentityv1alpha1.OIDCIssuer) (oidc.PublishedDocuments, error) {
	p.publishes.Add(1)
	return oidc.PublishedDocuments{IssuerURL: testIssuerURL}, nil
}

func (*countingOIDCPublisher) Delete(context.Context, *workloadidentityv1alpha1.OIDCIssuer) error {
	return nil
}

type countingWorkloadIdentityManager struct {
	ensures atomic.Int32
}

func (m *countingWorkloadIdentityManager) Ensure(context.Context, *workloadidentityv1alpha1.WorkloadIdentity, string, string) (workloadidentity.ManagedIdentity, error) {
	m.ensures.Add(1)
	return workloadidentity.ManagedIdentity{ClientID: testClientID, TenantID: testTenantID}, nil
}

func (*countingWorkloadIdentityManager) Delete(context.Context, *workloadidentityv1alpha1.WorkloadIdentity) error {
	return nil
}

func (*countingWorkloadIdentityManager) DeleteWithOptions(context.Context, *workloadidentityv1alpha1.WorkloadIdentity, workloadidentity.DeleteOptions) error {
	return nil
}

var _ = Describe("Controller manager event settling", Ordered, func() {
	It("keeps periodic heartbeats from causing self or cross-controller hot loops", func() {
		const refreshInterval = time.Hour
		manager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
		})
		Expect(err).NotTo(HaveOccurred())

		publisher := &countingOIDCPublisher{}
		workloadManager := &countingWorkloadIdentityManager{}
		Expect((&OIDCIssuerReconciler{
			Client:                    manager.GetClient(),
			Scheme:                    manager.GetScheme(),
			Publisher:                 publisher,
			OIDCIssuerRefreshInterval: refreshInterval,
		}).SetupWithManager(manager)).To(Succeed())
		Expect((&WorkloadIdentityReconciler{
			Client:          manager.GetClient(),
			Scheme:          manager.GetScheme(),
			Manager:         workloadManager,
			RefreshInterval: refreshInterval,
		}).SetupWithManager(manager)).To(Succeed())

		managerContext, stopManager := context.WithCancel(ctx)
		managerDone := make(chan error, 1)
		go func() {
			managerDone <- manager.Start(managerContext)
		}()
		Expect(manager.GetCache().WaitForCacheSync(managerContext)).To(BeTrue())

		namespace := &corev1.Namespace{}
		namespace.Name = "controller-settling"
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		identityKey := types.NamespacedName{Name: "settling-workload", Namespace: namespace.Name}
		issuerKey := types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}

		DeferCleanup(func() {
			stopManager()
			Eventually(managerDone, 10*time.Second).Should(Receive(BeNil()))
			deleteWorkloadIdentity(ctx, identityKey)
			deleteOIDCIssuer(ctx, issuerKey)
			Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
		})

		Expect(k8sClient.Create(ctx, validOIDCIssuer(issuerKey.Name))).To(Succeed())
		identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
		Expect(k8sClient.Create(ctx, identity)).To(Succeed())

		Eventually(publisher.publishes.Load, 5*time.Second, 25*time.Millisecond).Should(BeNumerically(">=", 1))
		Eventually(workloadManager.ensures.Load, 5*time.Second, 25*time.Millisecond).Should(BeNumerically(">=", 1))
		Eventually(func() bool {
			publishes := publisher.publishes.Load()
			ensures := workloadManager.ensures.Load()
			time.Sleep(150 * time.Millisecond)
			return publisher.publishes.Load() == publishes && workloadManager.ensures.Load() == ensures
		}, 5*time.Second, 25*time.Millisecond).Should(BeTrue())

		initialPublishes := publisher.publishes.Load()
		initialEnsures := workloadManager.ensures.Load()
		Consistently(func() bool {
			return publisher.publishes.Load() == initialPublishes && workloadManager.ensures.Load() == initialEnsures
		}, 500*time.Millisecond, 25*time.Millisecond).Should(BeTrue())

		persistedIdentity := &workloadidentityv1alpha1.WorkloadIdentity{}
		Expect(k8sClient.Get(ctx, identityKey, persistedIdentity)).To(Succeed())
		now := metav1.Now()
		persistedIdentity.Status.LastReconciledTime = &now
		Expect(k8sClient.Status().Update(ctx, persistedIdentity)).To(Succeed())
		persistedIssuer := &workloadidentityv1alpha1.OIDCIssuer{}
		Expect(k8sClient.Get(ctx, issuerKey, persistedIssuer)).To(Succeed())
		persistedIssuer.Status.LastReconciledTime = &now
		Expect(k8sClient.Status().Update(ctx, persistedIssuer)).To(Succeed())

		Consistently(func() bool {
			return publisher.publishes.Load() == initialPublishes && workloadManager.ensures.Load() == initialEnsures
		}, 500*time.Millisecond, 25*time.Millisecond).Should(BeTrue())
	})

	It("runs exactly one reconcile at each periodic WorkloadIdentity timer", func() {
		const refreshInterval = 3 * time.Second
		skipNameValidation := true
		manager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			Controller:             controllerconfig.Controller{SkipNameValidation: &skipNameValidation},
		})
		Expect(err).NotTo(HaveOccurred())

		workloadManager := &countingWorkloadIdentityManager{}
		Expect((&WorkloadIdentityReconciler{
			Client:          manager.GetClient(),
			Scheme:          manager.GetScheme(),
			Manager:         workloadManager,
			RefreshInterval: refreshInterval,
		}).SetupWithManager(manager)).To(Succeed())

		namespace := &corev1.Namespace{}
		namespace.Name = "controller-periodic"
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		identityKey := types.NamespacedName{Name: "periodic-workload", Namespace: namespace.Name}
		issuerKey := types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}
		createReadyOIDCIssuer(ctx, issuerKey.Name)
		identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
		Expect(k8sClient.Create(ctx, identity)).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name:        identity.Spec.ServiceAccount.Name,
			Namespace:   identity.Namespace,
			Labels:      desiredServiceAccountLabels(identity, true),
			Annotations: desiredServiceAccountAnnotations(workloadidentity.ManagedIdentity{ClientID: testClientID, TenantID: testTenantID}),
		}})).To(Succeed())

		managerContext, stopManager := context.WithCancel(ctx)
		managerDone := make(chan error, 1)
		go func() {
			managerDone <- manager.Start(managerContext)
		}()
		Expect(manager.GetCache().WaitForCacheSync(managerContext)).To(BeTrue())

		DeferCleanup(func() {
			stopManager()
			Eventually(managerDone, 10*time.Second).Should(Receive(BeNil()))
			deleteWorkloadIdentity(ctx, identityKey)
			deleteOIDCIssuer(ctx, issuerKey)
			Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
		})

		Eventually(workloadManager.ensures.Load, 5*time.Second, 25*time.Millisecond).Should(Equal(int32(1)))
		Consistently(workloadManager.ensures.Load, 500*time.Millisecond, 25*time.Millisecond).Should(Equal(int32(1)))
		Eventually(workloadManager.ensures.Load, 4*time.Second, 25*time.Millisecond).Should(Equal(int32(2)))
		Consistently(workloadManager.ensures.Load, 500*time.Millisecond, 25*time.Millisecond).Should(Equal(int32(2)))
	})
})
