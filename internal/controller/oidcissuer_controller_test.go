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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/oidc"
)

const (
	testIssuerURL         = "https://oidctest123.blob.core.windows.net/oidc"
	testPreviousIssuerURL = "https://previous.example"
)

type fakeOIDCDocumentPublisher struct {
	published oidc.PublishedDocuments
	publishes int
	deletes   int
	calls     *[]string
}

type fakeOpenShiftServiceAccountIssuer struct {
	currentIssuer string
	setIssuerURL  string
	rolloutErrs   []error
	gets          int
	sets          int
	rolloutWaits  int
	beforeSet     func()
	calls         *[]string
}

type fakeServiceAccountTokenClient struct {
	currentIssuer string
	err           error
	gets          int
}

func (f *fakeOpenShiftServiceAccountIssuer) Get(context.Context) (string, error) {
	f.gets++
	return f.currentIssuer, nil
}

func (f *fakeServiceAccountTokenClient) CurrentIssuer(context.Context) (string, error) {
	f.gets++
	return f.currentIssuer, f.err
}

func (f *fakeOpenShiftServiceAccountIssuer) Set(_ context.Context, issuerURL string) (bool, error) {
	f.sets++
	if f.beforeSet != nil {
		f.beforeSet()
	}
	changed := f.currentIssuer != issuerURL
	f.setIssuerURL = issuerURL
	f.currentIssuer = issuerURL
	if f.calls != nil {
		*f.calls = append(*f.calls, "set-service-account-issuer")
	}
	return changed, nil
}

func (f *fakeOpenShiftServiceAccountIssuer) WaitForKubeAPIServerRollout(context.Context, time.Time) error {
	f.rolloutWaits++
	if f.calls != nil {
		*f.calls = append(*f.calls, "wait-kube-apiserver-rollout")
	}
	if len(f.rolloutErrs) > 0 {
		err := f.rolloutErrs[0]
		f.rolloutErrs = f.rolloutErrs[1:]
		return err
	}
	return nil
}

func (f *fakeOIDCDocumentPublisher) Publish(context.Context, *workloadidentityv1alpha1.OIDCIssuer) (oidc.PublishedDocuments, error) {
	f.publishes++
	return f.published, nil
}

func (f *fakeOIDCDocumentPublisher) Delete(context.Context, *workloadidentityv1alpha1.OIDCIssuer) error {
	f.deletes++
	if f.calls != nil {
		*f.calls = append(*f.calls, "delete-azure-oidc-resources")
	}
	return nil
}

var _ = Describe("OIDCIssuer Controller", func() {
	Context("When reconciling a resource", func() {
		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}
		blockingIdentityKey := types.NamespacedName{Name: "blocking-workload", Namespace: "default"}

		BeforeEach(func() {
			deleteWorkloadIdentity(ctx, blockingIdentityKey)
			deleteOIDCIssuer(ctx, typeNamespacedName)
		})

		AfterEach(func() {
			deleteWorkloadIdentity(ctx, blockingIdentityKey)
			deleteOIDCIssuer(ctx, typeNamespacedName)
		})

		It("publishes documents and marks the singleton resource ready", func() {
			publisher := &fakeOIDCDocumentPublisher{
				published: oidc.PublishedDocuments{
					IssuerURL: testIssuerURL,
					AzureResources: []workloadidentityv1alpha1.AzureResource{{
						ID:   "/subscriptions/test/resourceGroups/rg-oidc-test",
						Kind: "ResourceGroup",
					}},
				},
			}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := newOIDCIssuerReconciler(publisher, nil)

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(defaultSigningKeyRefreshInterval))
			Expect(publisher.publishes).To(Equal(1))

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			Expect(updated.Status.IssuerURL).To(Equal(testIssuerURL))
			Expect(updated.Status.AzureResources).To(HaveLen(1))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("Published"))
		})

		It("uses a custom signing key refresh interval", func() {
			publisher := &fakeOIDCDocumentPublisher{published: oidc.PublishedDocuments{IssuerURL: testIssuerURL}}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := &OIDCIssuerReconciler{
				Client:                    k8sClient,
				Scheme:                    k8sClient.Scheme(),
				Publisher:                 publisher,
				SigningKeyRefreshInterval: time.Minute,
			}

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Minute))
		})

		It("persists the issuer URL and previous OpenShift service account issuer before setting the published issuer", func() {
			publisher := &fakeOIDCDocumentPublisher{published: oidc.PublishedDocuments{IssuerURL: testIssuerURL}}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{
				currentIssuer: testPreviousIssuerURL,
				beforeSet: func() {
					persisted := &workloadidentityv1alpha1.OIDCIssuer{}
					Expect(k8sClient.Get(ctx, typeNamespacedName, persisted)).To(Succeed())
					Expect(persisted.Status.IssuerURL).To(Equal(testIssuerURL))
					Expect(persisted.Status.PreviousServiceAccountIssuer).NotTo(BeNil())
					Expect(*persisted.Status.PreviousServiceAccountIssuer).To(Equal(testPreviousIssuerURL))
				},
			}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			resource.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: true}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(1))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(1))
			Expect(openShiftServiceAccountIssuer.setIssuerURL).To(Equal(testIssuerURL))
			Expect(openShiftServiceAccountIssuer.rolloutWaits).To(Equal(1))

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.PreviousServiceAccountIssuer).NotTo(BeNil())
			Expect(*updated.Status.PreviousServiceAccountIssuer).To(Equal(testPreviousIssuerURL))
		})

		It("blocks deletion after OpenShift issuer update succeeds but rollout wait fails", func() {
			publisher := &fakeOIDCDocumentPublisher{published: oidc.PublishedDocuments{IssuerURL: testIssuerURL}}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{
				currentIssuer: testPreviousIssuerURL,
				rolloutErrs:   []error{fmt.Errorf("rollout timed out")},
			}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			resource.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: true}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(MatchError(ContainSubstring("rollout timed out")))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(1))
			Expect(openShiftServiceAccountIssuer.rolloutWaits).To(Equal(1))

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.IssuerURL).To(Equal(testIssuerURL))
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(publisher.deletes).To(Equal(0))

			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("BlockedByOpenShiftServiceAccountIssuer"))
			Expect(condition.Message).To(ContainSubstring(testIssuerURL))
		})

		It("does not capture a previous OpenShift service account issuer when it already matches", func() {
			publisher := &fakeOIDCDocumentPublisher{published: oidc.PublishedDocuments{IssuerURL: testIssuerURL}}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{currentIssuer: testIssuerURL}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			resource.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: true}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(1))

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.PreviousServiceAccountIssuer).To(BeNil())
		})

		It("does not update OpenShift service account issuer when disabled", func() {
			publisher := &fakeOIDCDocumentPublisher{published: oidc.PublishedDocuments{IssuerURL: testIssuerURL}}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(0))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(0))
		})

		It("marks non-default OIDCIssuers invalid", func() {
			invalidName := "not-default"
			invalidKey := types.NamespacedName{Name: invalidName}
			resource := validOIDCIssuer(invalidName)
			resource.Spec.Azure.StorageAccountName = "oidctest124"
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			DeferCleanup(func() {
				deleteOIDCIssuer(ctx, invalidKey)
			})

			controllerReconciler := newOIDCIssuerReconciler(nil, nil)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: invalidKey})
			Expect(err).NotTo(HaveOccurred())

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, invalidKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("InvalidName"))
		})

		It("deletes published documents when deletion policy is Delete", func() {
			publisher := &fakeOIDCDocumentPublisher{}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			resource.Finalizers = []string{oidcIssuerFinalizer}
			resource.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			controllerReconciler := newOIDCIssuerReconciler(publisher, nil)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(publisher.deletes).To(Equal(1))
		})

		It("blocks deletion while WorkloadIdentities still exist", func() {
			previousIssuer := testPreviousIssuerURL
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{currentIssuer: testIssuerURL, calls: &calls}
			Expect(k8sClient.Create(ctx, validWorkloadIdentity(blockingIdentityKey.Name, blockingIdentityKey.Namespace))).To(Succeed())
			createDeletingOIDCIssuer(ctx, typeNamespacedName, &previousIssuer)

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(0))
			Expect(openShiftServiceAccountIssuer.rolloutWaits).To(Equal(0))
			Expect(publisher.deletes).To(Equal(0))
			Expect(calls).To(BeEmpty())

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("BlockedByWorkloadIdentities"))
			Expect(condition.Message).To(ContainSubstring("default/blocking-workload"))
		})

		It("blocks deletion while the cluster still mints service account tokens with the issuer URL", func() {
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			serviceAccountTokens := &fakeServiceAccountTokenClient{currentIssuer: testIssuerURL}
			createDeletingOIDCIssuer(ctx, typeNamespacedName, nil)

			controllerReconciler := newOIDCIssuerReconcilerWithTokenClient(publisher, nil, serviceAccountTokens)

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(defaultServiceAccountIssuerCheckPeriod))
			Expect(serviceAccountTokens.gets).To(Equal(1))
			Expect(publisher.deletes).To(Equal(0))
			Expect(calls).To(BeEmpty())

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("BlockedByClusterServiceAccountIssuer"))
			Expect(condition.Message).To(ContainSubstring(testIssuerURL))
		})

		It("blocks deletion when no cluster service account issuer guard is configured", func() {
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			createDeletingOIDCIssuerWithoutOpenShift(ctx, typeNamespacedName)

			controllerReconciler := newOIDCIssuerReconcilerWithTokenClient(publisher, nil, nil)

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(defaultServiceAccountIssuerCheckPeriod))
			Expect(publisher.deletes).To(Equal(0))
			Expect(calls).To(BeEmpty())

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("ClusterServiceAccountIssuerGuardUnavailable"))
			Expect(condition.Message).To(ContainSubstring("cannot verify"))
			Expect(condition.Message).To(ContainSubstring(testIssuerURL))
		})

		It("deletes published documents after the cluster service account token issuer is handed off", func() {
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			serviceAccountTokens := &fakeServiceAccountTokenClient{currentIssuer: testPreviousIssuerURL}
			createDeletingOIDCIssuer(ctx, typeNamespacedName, nil)

			controllerReconciler := newOIDCIssuerReconcilerWithTokenClient(publisher, nil, serviceAccountTokens)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(serviceAccountTokens.gets).To(Equal(1))
			Expect(publisher.deletes).To(Equal(1))
			Expect(calls).To(Equal([]string{"delete-azure-oidc-resources"}))
		})

		It("keeps the finalizer when the cluster service account token issuer cannot be verified", func() {
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			serviceAccountTokens := &fakeServiceAccountTokenClient{err: fmt.Errorf("token request forbidden")}
			createDeletingOIDCIssuer(ctx, typeNamespacedName, nil)

			controllerReconciler := newOIDCIssuerReconcilerWithTokenClient(publisher, nil, serviceAccountTokens)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(MatchError(ContainSubstring("token request forbidden")))
			Expect(serviceAccountTokens.gets).To(Equal(1))
			Expect(publisher.deletes).To(Equal(0))
			Expect(calls).To(BeEmpty())

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("ClusterServiceAccountIssuerCheckFailed"))
			Expect(condition.Message).To(ContainSubstring(testIssuerURL))
		})

		It("requeues the singleton OIDCIssuer when a WorkloadIdentity changes", func() {
			controllerReconciler := newOIDCIssuerReconciler(nil, nil)

			requests := controllerReconciler.oidcIssuerForWorkloadIdentity(ctx, validWorkloadIdentity(blockingIdentityKey.Name, blockingIdentityKey.Namespace))

			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: typeNamespacedName}))
		})

		It("requeues the singleton OIDCIssuer when Authentication cluster changes", func() {
			controllerReconciler := newOIDCIssuerReconciler(nil, nil)

			requests := controllerReconciler.oidcIssuerForAuthentication(ctx, &configv1.Authentication{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})
			ignoredRequests := controllerReconciler.oidcIssuerForAuthentication(ctx, &configv1.Authentication{ObjectMeta: metav1.ObjectMeta{Name: "not-cluster"}})

			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: typeNamespacedName}))
			Expect(ignoredRequests).To(BeEmpty())
		})

		It("blocks deletion while OpenShift still uses the issuer URL", func() {
			previousIssuer := testPreviousIssuerURL
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{currentIssuer: testIssuerURL, calls: &calls}
			createDeletingOIDCIssuer(ctx, typeNamespacedName, &previousIssuer)

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(1))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(0))
			Expect(openShiftServiceAccountIssuer.rolloutWaits).To(Equal(0))
			Expect(publisher.deletes).To(Equal(0))
			Expect(calls).To(BeEmpty())

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("BlockedByOpenShiftServiceAccountIssuer"))
			Expect(condition.Message).To(ContainSubstring(testIssuerURL))
		})

		It("blocks deletion while OpenShift still uses the issuer URL even when OpenShift spec is removed", func() {
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{currentIssuer: testIssuerURL, calls: &calls}
			createDeletingOIDCIssuerWithoutOpenShift(ctx, typeNamespacedName)

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(1))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(0))
			Expect(openShiftServiceAccountIssuer.rolloutWaits).To(Equal(0))
			Expect(publisher.deletes).To(Equal(0))
			Expect(calls).To(BeEmpty())

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("BlockedByOpenShiftServiceAccountIssuer"))
		})

		It("deletes published documents after OpenShift service account issuer is handed off", func() {
			previousIssuer := testPreviousIssuerURL
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{currentIssuer: previousIssuer, calls: &calls}
			createDeletingOIDCIssuer(ctx, typeNamespacedName, &previousIssuer)

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(1))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(0))
			Expect(openShiftServiceAccountIssuer.rolloutWaits).To(Equal(0))
			Expect(publisher.deletes).To(Equal(1))
			Expect(calls).To(Equal([]string{"delete-azure-oidc-resources"}))
		})

		It("blocks deletion policy Retain while OpenShift still uses the issuer URL", func() {
			previousIssuer := ""
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{currentIssuer: testIssuerURL, calls: &calls}
			createDeletingOIDCIssuerWithPolicy(ctx, typeNamespacedName, &previousIssuer, workloadidentityv1alpha1.DeletionPolicyRetain)

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(1))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(0))
			Expect(openShiftServiceAccountIssuer.rolloutWaits).To(Equal(0))
			Expect(publisher.deletes).To(Equal(0))
			Expect(calls).To(BeEmpty())

			updated := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(oidcIssuerFinalizer))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.OIDCIssuerConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("BlockedByOpenShiftServiceAccountIssuer"))
		})

		It("allows deletion policy Retain after OpenShift service account issuer is handed off", func() {
			previousIssuer := testPreviousIssuerURL
			calls := []string{}
			publisher := &fakeOIDCDocumentPublisher{calls: &calls}
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuer{currentIssuer: previousIssuer, calls: &calls}
			createDeletingOIDCIssuerWithPolicy(ctx, typeNamespacedName, &previousIssuer, workloadidentityv1alpha1.DeletionPolicyRetain)

			controllerReconciler := newOIDCIssuerReconciler(publisher, openShiftServiceAccountIssuer)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(1))
			Expect(openShiftServiceAccountIssuer.sets).To(Equal(0))
			Expect(openShiftServiceAccountIssuer.rolloutWaits).To(Equal(0))
			Expect(publisher.deletes).To(Equal(0))
			Expect(calls).To(BeEmpty())
		})

	})
})

func newOIDCIssuerReconciler(publisher oidc.Publisher, openShiftServiceAccountIssuer OpenShiftServiceAccountIssuerClient) *OIDCIssuerReconciler {
	return newOIDCIssuerReconcilerWithTokenClient(publisher, openShiftServiceAccountIssuer, nil)
}

func newOIDCIssuerReconcilerWithTokenClient(publisher oidc.Publisher, openShiftServiceAccountIssuer OpenShiftServiceAccountIssuerClient, serviceAccountTokens ServiceAccountTokenClient) *OIDCIssuerReconciler {
	return &OIDCIssuerReconciler{
		Client:                        k8sClient,
		Scheme:                        k8sClient.Scheme(),
		Publisher:                     publisher,
		OpenShiftServiceAccountIssuer: openShiftServiceAccountIssuer,
		ServiceAccountTokens:          serviceAccountTokens,
	}
}

func validOIDCIssuer(name string) *workloadidentityv1alpha1.OIDCIssuer {
	return &workloadidentityv1alpha1.OIDCIssuer{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: workloadidentityv1alpha1.OIDCIssuerSpec{
			Azure: workloadidentityv1alpha1.AzureOIDCIssuerConfig{
				SubscriptionID:     "00000000-0000-0000-0000-000000000000",
				Location:           "swedencentral",
				ResourceGroupName:  "rg-oidc-test",
				StorageAccountName: "oidctest123",
				BlobContainerName:  "oidc",
			},
			SigningKey: workloadidentityv1alpha1.SigningKeySource{
				SecretRef: workloadidentityv1alpha1.SecretKeyReference{
					Name:      "service-account-signing-key",
					Namespace: "kube-system",
					Key:       "tls.key",
				},
			},
		},
	}
}

func createDeletingOIDCIssuer(ctx context.Context, key types.NamespacedName, previousIssuer *string) {
	createDeletingOIDCIssuerWithPolicy(ctx, key, previousIssuer, workloadidentityv1alpha1.DeletionPolicyDelete)
}

func createDeletingOIDCIssuerWithoutOpenShift(ctx context.Context, key types.NamespacedName) {
	issuer := validOIDCIssuer(key.Name)
	issuer.Finalizers = []string{oidcIssuerFinalizer}
	issuer.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
	Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
	patchOIDCIssuerStatus(ctx, key, func(status *workloadidentityv1alpha1.OIDCIssuerStatus) {
		status.IssuerURL = testIssuerURL
	})
	Expect(k8sClient.Delete(ctx, issuer)).To(Succeed())
}

func createDeletingOIDCIssuerWithPolicy(ctx context.Context, key types.NamespacedName, previousIssuer *string, policy workloadidentityv1alpha1.DeletionPolicy) {
	issuer := validOIDCIssuer(key.Name)
	issuer.Finalizers = []string{oidcIssuerFinalizer}
	issuer.Spec.DeletionPolicy = policy
	issuer.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: true}
	Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
	patchOIDCIssuerStatus(ctx, key, func(status *workloadidentityv1alpha1.OIDCIssuerStatus) {
		status.IssuerURL = testIssuerURL
		status.PreviousServiceAccountIssuer = previousIssuer
	})
	Expect(k8sClient.Delete(ctx, issuer)).To(Succeed())
}

func deleteOIDCIssuer(ctx context.Context, key types.NamespacedName) {
	issuer := &workloadidentityv1alpha1.OIDCIssuer{}
	if err := k8sClient.Get(ctx, key, issuer); err != nil {
		Expect(errors.IsNotFound(err)).To(BeTrue())
		return
	}

	if len(issuer.Finalizers) > 0 {
		patch := client.MergeFrom(issuer.DeepCopy())
		issuer.Finalizers = nil
		Expect(k8sClient.Patch(ctx, issuer, patch)).To(Succeed())
	}

	err := k8sClient.Delete(ctx, issuer)
	if err != nil {
		Expect(errors.IsNotFound(err)).To(BeTrue(), fmt.Sprintf("unexpected delete error: %v", err))
	}
}

func patchOIDCIssuerStatus(ctx context.Context, key types.NamespacedName, mutate func(*workloadidentityv1alpha1.OIDCIssuerStatus)) {
	issuer := &workloadidentityv1alpha1.OIDCIssuer{}
	Expect(k8sClient.Get(ctx, key, issuer)).To(Succeed())
	patch := client.MergeFrom(issuer.DeepCopy())
	mutate(&issuer.Status)
	Expect(k8sClient.Status().Patch(ctx, issuer, patch)).To(Succeed())
}
