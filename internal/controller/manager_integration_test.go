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
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
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

type switchableWorkloadIdentityManager struct {
	mu      sync.RWMutex
	managed workloadidentity.ManagedIdentity
	err     error
	ensures atomic.Int32
}

func (m *switchableWorkloadIdentityManager) Ensure(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentity,
	string,
	string,
) (workloadidentity.ManagedIdentity, error) {
	m.ensures.Add(1)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.managed, m.err
}

func (*switchableWorkloadIdentityManager) Delete(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentity,
) error {
	return nil
}

func (m *switchableWorkloadIdentityManager) set(
	managed workloadidentity.ManagedIdentity,
	err error,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.managed = managed
	m.err = err
}

type switchableRecoveryDetector struct {
	mu       sync.RWMutex
	evidence workloadidentity.RecoveryRequiredEvidence
	err      error
	calls    atomic.Int32
}

func (d *switchableRecoveryDetector) DetectRecovery(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentity,
) (workloadidentity.RecoveryRequiredEvidence, error) {
	d.calls.Add(1)
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.evidence, d.err
}

func (d *switchableRecoveryDetector) set(
	evidence workloadidentity.RecoveryRequiredEvidence,
	err error,
) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.evidence = evidence
	d.err = err
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

	It("resumes WorkloadIdentity reconciliation after recovery cancellation without a ServiceAccount event", func() {
		const refreshInterval = time.Hour
		skipNameValidation := true
		manager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			Controller:             controllerconfig.Controller{SkipNameValidation: &skipNameValidation},
		})
		Expect(err).NotTo(HaveOccurred())

		previousUID := types.UID(testPreviousWorkloadIdentityUID)
		detector := &switchableRecoveryDetector{}
		detector.set(workloadidentity.RecoveryRequiredEvidence{
			PreviousWorkloadIdentityUID: previousUID,
		}, nil)
		Expect((&WorkloadIdentityReconciler{
			Client:           manager.GetClient(),
			Scheme:           manager.GetScheme(),
			Manager:          &countingWorkloadIdentityManager{},
			RecoveryDetector: detector,
			RefreshInterval:  refreshInterval,
		}).SetupWithManager(manager)).To(Succeed())

		namespace := &corev1.Namespace{}
		namespace.Name = "controller-recovery-resume"
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		identityKey := types.NamespacedName{Name: "recovering-workload", Namespace: namespace.Name}
		issuerKey := types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}
		serviceAccountKey := types.NamespacedName{Name: testServiceAccountName, Namespace: namespace.Name}
		createReadyOIDCIssuer(ctx, issuerKey.Name)

		identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
		Expect(k8sClient.Create(ctx, identity)).To(Succeed())
		Expect(k8sClient.Get(ctx, identityKey, identity)).To(Succeed())
		identity.Status.Recovery = &workloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
			PreviousWorkloadIdentityUID: previousUID,
		}
		apimeta.SetStatusCondition(&identity.Status.Conditions, metav1.Condition{
			Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             workloadidentity.ReasonRecoveryRequired,
			Message:            "Recovery is required",
			ObservedGeneration: identity.Generation,
		})
		Expect(k8sClient.Status().Update(ctx, identity)).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountKey.Name,
			Namespace: serviceAccountKey.Namespace,
			Labels: map[string]string{
				serviceAccountUseLabel:  trueValue,
				serviceAccountManagedBy: serviceAccountManagerName,
				serviceAccountUID:       string(previousUID),
				serviceAccountCreatedBy: trueValue,
			},
			Annotations: map[string]string{
				serviceAccountClientID: testClientID,
				serviceAccountTenantID: testTenantID,
			},
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
			deleteServiceAccount(ctx, serviceAccountKey)
			deleteWorkloadIdentity(ctx, identityKey)
			deleteOIDCIssuer(ctx, issuerKey)
			Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
		})

		Eventually(detector.calls.Load, 5*time.Second, 25*time.Millisecond).Should(BeNumerically(">=", 1))
		Eventually(func() bool {
			calls := detector.calls.Load()
			time.Sleep(150 * time.Millisecond)
			return detector.calls.Load() == calls
		}, 5*time.Second, 25*time.Millisecond).Should(BeTrue())
		initialCalls := detector.calls.Load()

		detector.set(
			workloadidentity.RecoveryRequiredEvidence{},
			workloadidentity.NewConflictError(
				workloadidentity.ReasonRecoveryInProgress,
				"UserAssignedIdentity recovery marker is active",
			),
		)
		persistedIdentity := &workloadidentityv1alpha1.WorkloadIdentity{}
		Expect(k8sClient.Get(ctx, identityKey, persistedIdentity)).To(Succeed())
		apimeta.SetStatusCondition(&persistedIdentity.Status.Conditions, metav1.Condition{
			Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             workloadidentity.ReasonRecoveryInProgress,
			Message:            "Controlled recovery is in progress",
			ObservedGeneration: persistedIdentity.Generation,
		})
		Expect(k8sClient.Status().Update(ctx, persistedIdentity)).To(Succeed())

		Consistently(detector.calls.Load, 500*time.Millisecond, 25*time.Millisecond).Should(Equal(initialCalls))
		Eventually(func(g Gomega) {
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			ready := apimeta.FindStatusCondition(
				current.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Reason).To(Equal(workloadidentity.ReasonRecoveryInProgress))
			g.Expect(ready.Message).To(Equal("Controlled recovery is in progress"))
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())

		detector.set(workloadidentity.RecoveryRequiredEvidence{
			PreviousWorkloadIdentityUID: previousUID,
		}, nil)
		Consistently(detector.calls.Load, 6*time.Second, 50*time.Millisecond).Should(Equal(initialCalls))
		Eventually(func(g Gomega) {
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			ready := apimeta.FindStatusCondition(
				current.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Reason).To(Equal(workloadidentity.ReasonRecoveryInProgress))
			g.Expect(ready.Message).To(Equal("Controlled recovery is in progress"))
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())
		postPollCalls := detector.calls.Load()

		Expect(k8sClient.Get(ctx, identityKey, persistedIdentity)).To(Succeed())
		apimeta.SetStatusCondition(&persistedIdentity.Status.Conditions, metav1.Condition{
			Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             recoveryReasonCancelled,
			Message:            "Controlled recovery was cancelled before mutation",
			ObservedGeneration: persistedIdentity.Generation,
		})
		Expect(k8sClient.Status().Update(ctx, persistedIdentity)).To(Succeed())

		Eventually(detector.calls.Load, 5*time.Second, 25*time.Millisecond).Should(BeNumerically(">=", postPollCalls+1))
		Eventually(func(g Gomega) {
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			ready := apimeta.FindStatusCondition(
				current.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Reason).To(Equal(workloadidentity.ReasonRecoveryRequired))
			g.Expect(current.Status.Recovery).NotTo(BeNil())
			g.Expect(current.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(previousUID))
		}, 10*time.Second, 25*time.Millisecond).Should(Succeed())
	})

	It("resumes missing-ServiceAccount reconciliation after recovery cancellation and completion", func() {
		const refreshInterval = time.Hour
		skipNameValidation := true
		manager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			Controller:             controllerconfig.Controller{SkipNameValidation: &skipNameValidation},
		})
		Expect(err).NotTo(HaveOccurred())

		previousUID := types.UID(testPreviousWorkloadIdentityUID)
		workloadManager := &switchableWorkloadIdentityManager{}
		workloadManager.set(workloadidentity.ManagedIdentity{}, workloadidentity.NewRecoveryRequiredError(
			"Earlier WorkloadIdentity owns the retained UserAssignedIdentity",
			previousUID,
		))
		Expect((&WorkloadIdentityReconciler{
			Client:          manager.GetClient(),
			Scheme:          manager.GetScheme(),
			Manager:         workloadManager,
			RefreshInterval: refreshInterval,
		}).SetupWithManager(manager)).To(Succeed())

		namespace := &corev1.Namespace{}
		namespace.Name = "controller-missing-service-account-recovery"
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		identityKey := types.NamespacedName{Name: "recovering-missing-service-account", Namespace: namespace.Name}
		issuerKey := types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}
		serviceAccountKey := types.NamespacedName{Name: testServiceAccountName, Namespace: namespace.Name}
		createReadyOIDCIssuer(ctx, issuerKey.Name)

		identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
		Expect(k8sClient.Create(ctx, identity)).To(Succeed())

		managerContext, stopManager := context.WithCancel(ctx)
		managerDone := make(chan error, 1)
		go func() {
			managerDone <- manager.Start(managerContext)
		}()
		Expect(manager.GetCache().WaitForCacheSync(managerContext)).To(BeTrue())

		DeferCleanup(func() {
			stopManager()
			Eventually(managerDone, 10*time.Second).Should(Receive(BeNil()))
			deleteServiceAccount(ctx, serviceAccountKey)
			deleteWorkloadIdentity(ctx, identityKey)
			deleteOIDCIssuer(ctx, issuerKey)
			Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
		})

		setReadyReason := func(reason, message string) {
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			apimeta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
				Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
				ObservedGeneration: current.Generation,
			})
			Expect(k8sClient.Status().Update(ctx, current)).To(Succeed())
		}
		expectReadyReason := func(g Gomega, reason string) {
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			ready := apimeta.FindStatusCondition(
				current.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Reason).To(Equal(reason))
		}
		waitForEnsureCallsToSettle := func() {
			Eventually(func() bool {
				calls := workloadManager.ensures.Load()
				time.Sleep(150 * time.Millisecond)
				return workloadManager.ensures.Load() == calls
			}, 5*time.Second, 25*time.Millisecond).Should(BeTrue())
		}

		Eventually(func(g Gomega) {
			expectReadyReason(g, workloadidentity.ReasonRecoveryRequired)
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			g.Expect(current.Status.Recovery).NotTo(BeNil())
			g.Expect(current.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(previousUID))
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())
		waitForEnsureCallsToSettle()
		requiredCalls := workloadManager.ensures.Load()
		Consistently(workloadManager.ensures.Load, 500*time.Millisecond, 25*time.Millisecond).Should(Equal(requiredCalls))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{}))).To(BeTrue())

		workloadManager.set(
			workloadidentity.ManagedIdentity{},
			workloadidentity.NewConflictError(
				workloadidentity.ReasonRecoveryInProgress,
				"UserAssignedIdentity recovery marker is active",
			),
		)
		setReadyReason(workloadidentity.ReasonRecoveryInProgress, "Controlled recovery is in progress")
		Consistently(workloadManager.ensures.Load, 500*time.Millisecond, 25*time.Millisecond).Should(Equal(requiredCalls))
		Eventually(func(g Gomega) {
			expectReadyReason(g, workloadidentity.ReasonRecoveryInProgress)
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			ready := apimeta.FindStatusCondition(
				current.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			g.Expect(ready.Message).To(Equal("Controlled recovery is in progress"))
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())

		workloadManager.set(workloadidentity.ManagedIdentity{}, workloadidentity.NewRecoveryRequiredError(
			"Earlier WorkloadIdentity owns the retained UserAssignedIdentity",
			previousUID,
		))
		Consistently(workloadManager.ensures.Load, 6*time.Second, 50*time.Millisecond).Should(Equal(requiredCalls))
		Eventually(func(g Gomega) {
			expectReadyReason(g, workloadidentity.ReasonRecoveryInProgress)
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			g.Expect(current.Status.Recovery).NotTo(BeNil())
			g.Expect(current.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(previousUID))
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())
		cancelledPollCalls := workloadManager.ensures.Load()

		setReadyReason(recoveryReasonCancelled, "Controlled recovery was cancelled before mutation")
		Eventually(workloadManager.ensures.Load, 5*time.Second, 25*time.Millisecond).Should(BeNumerically(">=", cancelledPollCalls+1))
		Eventually(func(g Gomega) {
			expectReadyReason(g, workloadidentity.ReasonRecoveryRequired)
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())
		waitForEnsureCallsToSettle()
		cancelledCalls := workloadManager.ensures.Load()
		Consistently(workloadManager.ensures.Load, 500*time.Millisecond, 25*time.Millisecond).Should(Equal(cancelledCalls))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{}))).To(BeTrue())

		workloadManager.set(
			workloadidentity.ManagedIdentity{},
			workloadidentity.NewConflictError(
				workloadidentity.ReasonRecoveryInProgress,
				"UserAssignedIdentity recovery marker is active",
			),
		)
		setReadyReason(workloadidentity.ReasonRecoveryInProgress, "Controlled recovery is in progress")
		Consistently(workloadManager.ensures.Load, 500*time.Millisecond, 25*time.Millisecond).Should(Equal(cancelledCalls))
		Eventually(func(g Gomega) {
			expectReadyReason(g, workloadidentity.ReasonRecoveryInProgress)
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())
		secondProgressCalls := workloadManager.ensures.Load()
		Consistently(workloadManager.ensures.Load, 500*time.Millisecond, 25*time.Millisecond).Should(Equal(secondProgressCalls))

		workloadManager.set(workloadidentity.ManagedIdentity{
			ClientID: testClientID,
			TenantID: testTenantID,
		}, nil)
		Consistently(workloadManager.ensures.Load, 6*time.Second, 50*time.Millisecond).Should(Equal(secondProgressCalls))
		Eventually(func(g Gomega) {
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			ready := apimeta.FindStatusCondition(
				current.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(workloadidentity.ReasonRecoveryInProgress))
			g.Expect(current.Status.Recovery).NotTo(BeNil())
			g.Expect(current.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(previousUID))
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{}))).To(BeTrue())
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())

		setReadyReason(recoveryReasonCompleted, "Controlled recovery completed; normal reconciliation will resume")
		Eventually(workloadManager.ensures.Load, 5*time.Second, 25*time.Millisecond).Should(BeNumerically(">=", secondProgressCalls+1))
		Eventually(func(g Gomega) {
			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			g.Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			ready := apimeta.FindStatusCondition(
				current.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(ready.Reason).To(Equal("Reconciled"))
			g.Expect(current.Status.Recovery).To(BeNil())
			g.Expect(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{})).To(Succeed())
		}, 5*time.Second, 25*time.Millisecond).Should(Succeed())
	})
})
