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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	testWorkloadIdentityIssuerURL = "https://oidctest123.blob.core.windows.net/oidc"
	testWorkloadNamespace         = "default"
	testServiceAccountName        = "test-sa"
	testOtherServiceAccountName   = "other-sa"
	testOtherFederatedCredential  = "other-fic"
	testClientID                  = "client-id"
	testTenantID                  = "tenant-id"
	testExistingLabel             = "existing"
)

type fakeWorkloadIdentityManager struct {
	managed  workloadidentity.ManagedIdentity
	err      error
	onEnsure func()
	ensures  int
	deletes  int
	subject  string
	options  workloadidentity.DeleteOptions
}

func (f *fakeWorkloadIdentityManager) Ensure(_ context.Context, _ *workloadidentityv1alpha1.WorkloadIdentity, _, subject string) (workloadidentity.ManagedIdentity, error) {
	f.ensures++
	f.subject = subject
	if f.onEnsure != nil {
		f.onEnsure()
	}
	return f.managed, f.err
}

func (f *fakeWorkloadIdentityManager) Delete(context.Context, *workloadidentityv1alpha1.WorkloadIdentity) error {
	f.deletes++
	return f.err
}

func (f *fakeWorkloadIdentityManager) DeleteWithOptions(_ context.Context, _ *workloadidentityv1alpha1.WorkloadIdentity, options workloadidentity.DeleteOptions) error {
	f.deletes++
	f.options = options
	return f.err
}

var _ = Describe("WorkloadIdentity Controller", func() {
	Describe("Refresh jitter", func() {
		It("is stable and bounded to ten percent", func() {
			interval := time.Minute
			first := jitteredWorkloadIdentityRefreshInterval(interval, "test-uid")
			second := jitteredWorkloadIdentityRefreshInterval(interval, "test-uid")

			Expect(first).To(Equal(second))
			Expect(first).To(BeNumerically(">=", interval))
			Expect(first).To(BeNumerically("<=", interval+interval/10))
		})

		It("scales high hash values without overflowing", func() {
			interval := 5 * time.Minute
			jitter := jitteredWorkloadIdentityRefreshInterval(interval, "uid-95408") - interval

			Expect(jitter).To(BeNumerically(">", 29*time.Second))
			Expect(jitter).To(BeNumerically("<=", 30*time.Second))
		})
	})

	Describe("Shared-parent deletion election", func() {
		It("can elect different coordinators for resource-group and identity scopes", func() {
			current := deletionReference{key: "default/b", uid: "uid-b", deleting: true}
			resourceGroupSuccessor, waitForResourceGroup := deletionSuccessor(current, []deletionReference{
				{key: "default/a", uid: "uid-a", deleting: true},
				{key: "default/c", uid: "uid-c", deleting: true},
			})
			identitySuccessor, waitForIdentity := deletionSuccessor(current, []deletionReference{
				{key: "default/c", uid: "uid-c", deleting: true},
			})

			Expect(resourceGroupSuccessor).To(Equal("uid-a"))
			Expect(waitForResourceGroup).To(BeFalse())
			Expect(identitySuccessor).To(BeEmpty())
			Expect(waitForIdentity).To(BeTrue())
		})
	})

	Describe("ServiceAccount provenance", func() {
		It("does not accept provenance without a recorded ServiceAccount subject", func() {
			identity := validWorkloadIdentity("test-workload", testWorkloadNamespace)
			identity.Status.ServiceAccountProvenance = workloadidentityv1alpha1.ServiceAccountProvenanceCreated

			_, recorded := persistedServiceAccountProvenance(identity)

			Expect(recorded).To(BeFalse())
		})

		It("does not carry provenance across a configured ServiceAccount name change", func() {
			identity := validWorkloadIdentity("test-workload", testWorkloadNamespace)
			identity.Status.Subject = "system:serviceaccount:default:test-sa"
			identity.Status.ServiceAccountProvenance = workloadidentityv1alpha1.ServiceAccountProvenanceCreated
			identity.Spec.ServiceAccount.Name = testOtherServiceAccountName

			_, recorded := persistedServiceAccountProvenance(identity)

			Expect(recorded).To(BeFalse())
		})
	})

	Context("When reconciling a resource", func() {
		ctx := context.Background()
		identityKey := types.NamespacedName{Name: "test-workload", Namespace: testWorkloadNamespace}
		otherIdentityKey := types.NamespacedName{Name: "other-workload", Namespace: testWorkloadNamespace}
		issuerKey := types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}
		serviceAccountKey := types.NamespacedName{Name: testServiceAccountName, Namespace: testWorkloadNamespace}

		BeforeEach(func() {
			deleteWorkloadIdentity(ctx, identityKey)
			deleteWorkloadIdentity(ctx, otherIdentityKey)
			deleteOIDCIssuer(ctx, issuerKey)
			deleteServiceAccount(ctx, serviceAccountKey)
		})

		AfterEach(func() {
			deleteWorkloadIdentity(ctx, identityKey)
			deleteWorkloadIdentity(ctx, otherIdentityKey)
			deleteOIDCIssuer(ctx, issuerKey)
			deleteServiceAccount(ctx, serviceAccountKey)
		})

		It("creates the ServiceAccount, ensures Azure resources, and marks ready", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{
				ClientID:    testClientID,
				PrincipalID: "principal-id",
				TenantID:    testTenantID,
				AzureResources: []workloadidentityv1alpha1.AzureResource{{
					ID:   "/subscriptions/test/resourceGroups/rg-wi-test/providers/Microsoft.ManagedIdentity/userAssignedIdentities/uami-test",
					Kind: "UserAssignedIdentity",
				}},
			}}

			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(jitteredWorkloadIdentityRefreshInterval(DefaultWorkloadIdentityRefreshInterval, string(identity.UID))))

			Expect(manager.ensures).To(Equal(1))
			Expect(manager.subject).To(Equal("system:serviceaccount:default:test-sa"))

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccount)).To(Succeed())
			Expect(serviceAccount.Labels[serviceAccountUseLabel]).To(Equal(trueValue))
			Expect(serviceAccount.Labels[serviceAccountCreatedBy]).To(Equal(trueValue))
			Expect(serviceAccount.Annotations[serviceAccountClientID]).To(Equal(testClientID))
			Expect(serviceAccount.Annotations[serviceAccountTenantID]).To(Equal(testTenantID))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ClientID).To(Equal(testClientID))
			Expect(updated.Status.Subject).To(Equal("system:serviceaccount:default:test-sa"))
			Expect(updated.Status.ServiceAccountUID).To(Equal(string(serviceAccount.UID)))
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceCreated))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		})

		It("adopts a ServiceAccount created while Azure resources are ensured", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{
				managed: workloadidentity.ManagedIdentity{ClientID: testClientID},
				onEnsure: func() {
					Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
						Name:      serviceAccountKey.Name,
						Namespace: serviceAccountKey.Namespace,
						Labels:    map[string]string{testExistingLabel: trueValue},
					}})).To(Succeed())
				},
			}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccount)).To(Succeed())
			Expect(serviceAccount.Labels[testExistingLabel]).To(Equal(trueValue))
			Expect(serviceAccount.Labels[serviceAccountCreatedBy]).To(Equal("false"))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceAdopted))
		})

		It("creates a ServiceAccount when the inspected account disappears while Azure resources are ensured", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			inspectedServiceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels:    map[string]string{testExistingLabel: trueValue},
			}}
			Expect(k8sClient.Create(ctx, inspectedServiceAccount)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{
				managed: workloadidentity.ManagedIdentity{ClientID: testClientID},
				onEnsure: func() {
					Expect(k8sClient.Delete(ctx, inspectedServiceAccount)).To(Succeed())
				},
			}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccount)).To(Succeed())
			Expect(serviceAccount.UID).NotTo(Equal(inspectedServiceAccount.UID))
			Expect(serviceAccount.Labels[testExistingLabel]).To(BeEmpty())
			Expect(serviceAccount.Labels[serviceAccountCreatedBy]).To(Equal(trueValue))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceCreated))
		})

		It("uses a custom workload identity refresh interval after successful reconciliation", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				Manager:         manager,
				RefreshInterval: time.Minute,
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(jitteredWorkloadIdentityRefreshInterval(time.Minute, string(identity.UID))))
		})

		It("updates the reconciliation timestamp when managed state is unchanged", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			serviceAccountBefore := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccountBefore)).To(Succeed())

			persisted := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, persisted)).To(Succeed())
			original := persisted.DeepCopy()
			staleTime := metav1.NewTime(time.Unix(1, 0))
			persisted.Status.LastReconciledTime = &staleTime
			Expect(k8sClient.Status().Patch(ctx, persisted, client.MergeFrom(original))).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.ensures).To(Equal(2))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.LastReconciledTime).NotTo(BeNil())
			Expect(updated.Status.LastReconciledTime.Time.After(staleTime.Time)).To(BeTrue())
			serviceAccountAfter := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccountAfter)).To(Succeed())
			Expect(serviceAccountAfter.ResourceVersion).To(Equal(serviceAccountBefore.ResourceVersion))
		})

		It("adopts a manually recreated ServiceAccount after successful reconciliation", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			originalServiceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, originalServiceAccount)).To(Succeed())
			originalUID := originalServiceAccount.UID
			Expect(k8sClient.Delete(ctx, originalServiceAccount)).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
			}})).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.ensures).To(Equal(2))

			replacement := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, replacement)).To(Succeed())
			Expect(replacement.UID).NotTo(Equal(originalUID))
			Expect(replacement.Labels[serviceAccountUID]).To(Equal(string(identity.UID)))
			Expect(replacement.Labels[serviceAccountCreatedBy]).To(Equal(trueValue))
			Expect(replacement.Annotations[serviceAccountClientID]).To(Equal(testClientID))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountUID).To(Equal(string(replacement.UID)))
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceCreated))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		})

		It("repairs Azure annotation drift on a recreated ServiceAccount", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			originalServiceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, originalServiceAccount)).To(Succeed())
			Expect(k8sClient.Delete(ctx, originalServiceAccount)).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Annotations: map[string]string{
					serviceAccountClientID: "other-client-id",
				},
			}})).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.ensures).To(Equal(2))

			replacement := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, replacement)).To(Succeed())
			Expect(replacement.Annotations[serviceAccountClientID]).To(Equal(testClientID))
			Expect(replacement.Labels[serviceAccountCreatedBy]).To(Equal(trueValue))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountUID).To(Equal(string(replacement.UID)))
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceCreated))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		})

		It("recreates a missing operator ServiceAccount and records its new UID", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			originalServiceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, originalServiceAccount)).To(Succeed())
			Expect(k8sClient.Delete(ctx, originalServiceAccount)).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			recreatedServiceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, recreatedServiceAccount)).To(Succeed())
			Expect(recreatedServiceAccount.UID).NotTo(Equal(originalServiceAccount.UID))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountUID).To(Equal(string(recreatedServiceAccount.UID)))
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceCreated))
		})

		It("does not restore created provenance without a matching WorkloadIdentity owner", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			persisted := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, persisted)).To(Succeed())
			original := persisted.DeepCopy()
			persisted.Status.Subject = serviceAccountSubject(persisted)
			persisted.Status.ServiceAccountUID = "previous-service-account-uid"
			Expect(k8sClient.Status().Patch(ctx, persisted, client.MergeFrom(original))).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountCreatedBy: trueValue,
				},
			}})).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccount)).To(Succeed())
			Expect(serviceAccount.Labels[serviceAccountCreatedBy]).To(Equal("false"))
			Expect(serviceAccount.Labels[serviceAccountUID]).To(Equal(string(identity.UID)))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceAdopted))
		})

		It("adopts an existing ServiceAccount without marking it as created", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels:    map[string]string{testExistingLabel: trueValue},
			}})).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccount)).To(Succeed())
			Expect(serviceAccount.Labels[testExistingLabel]).To(Equal(trueValue))
			Expect(serviceAccount.Labels[serviceAccountUseLabel]).To(Equal(trueValue))
			Expect(serviceAccount.Labels[serviceAccountCreatedBy]).To(Equal("false"))
			Expect(serviceAccount.Annotations[serviceAccountClientID]).To(Equal(testClientID))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceAdopted))
		})

		It("preserves adopted provenance when recreating a missing ServiceAccount", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
			}})).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			adopted := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, adopted)).To(Succeed())
			Expect(k8sClient.Delete(ctx, adopted)).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			recreated := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, recreated)).To(Succeed())
			Expect(recreated.UID).NotTo(Equal(adopted.UID))
			Expect(recreated.Labels[serviceAccountCreatedBy]).To(Equal("false"))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceAdopted))
		})

		It("marks not ready when the ServiceAccount is owned by another WorkloadIdentity", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountManagedBy: serviceAccountManagerName,
					serviceAccountUID:       "other-workload-identity-uid",
				},
			}})).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).To(MatchError(ContainSubstring("already managed by another WorkloadIdentity")))
			Expect(manager.ensures).To(Equal(0))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("ServiceAccountConflict"))
		})

		It("does not overwrite an adopted ServiceAccount annotated for another Azure identity", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Annotations: map[string]string{
					serviceAccountClientID: "other-client-id",
				},
			}})).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).To(MatchError(ContainSubstring("already annotated for Azure client ID")))
			Expect(manager.ensures).To(Equal(0))

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccount)).To(Succeed())
			Expect(serviceAccount.Labels).NotTo(HaveKey(serviceAccountUID))
			Expect(serviceAccount.Annotations[serviceAccountClientID]).To(Equal("other-client-id"))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("ServiceAccountConflict"))
		})

		It("maps Azure conflict errors to stable status reasons", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{
				err: workloadidentity.NewConflictError(
					workloadidentity.ReasonFederatedIdentityCredentialConflict,
					"federated identity credential conflicts with an existing trust tuple",
				),
			}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).To(MatchError(ContainSubstring("federated identity credential conflicts")))
			Expect(manager.ensures).To(Equal(1))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(workloadidentity.ReasonFederatedIdentityCredentialConflict))
		})

		It("waits when the OIDCIssuer is not ready", func() {
			issuer := validOIDCIssuer(issuerKey.Name)
			Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).NotTo(BeZero())
			Expect(manager.ensures).To(Equal(0))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("OIDCIssuerNotReady"))
		})

		It("enqueues WorkloadIdentities when the default OIDCIssuer changes", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			otherIdentity := validWorkloadIdentity(otherIdentityKey.Name, otherIdentityKey.Namespace)
			Expect(k8sClient.Create(ctx, otherIdentity)).To(Succeed())

			reconciler := &WorkloadIdentityReconciler{Client: indexedWorkloadIdentityClient(identity, otherIdentity), Scheme: k8sClient.Scheme()}
			requests := reconciler.workloadIdentitiesForOIDCIssuer(ctx, &workloadidentityv1alpha1.OIDCIssuer{ObjectMeta: metav1.ObjectMeta{Name: issuerKey.Name}})

			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: identityKey}, reconcile.Request{NamespacedName: otherIdentityKey}))
			Expect(reconciler.workloadIdentitiesForOIDCIssuer(ctx, &workloadidentityv1alpha1.OIDCIssuer{ObjectMeta: metav1.ObjectMeta{Name: "other"}})).To(BeNil())
		})

		It("enqueues matching WorkloadIdentities when a ServiceAccount changes without operator labels", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			otherIdentity := validWorkloadIdentity(otherIdentityKey.Name, otherIdentityKey.Namespace)
			otherIdentity.Spec.ServiceAccount.Name = testOtherServiceAccountName
			Expect(k8sClient.Create(ctx, otherIdentity)).To(Succeed())

			reconciler := &WorkloadIdentityReconciler{Client: indexedWorkloadIdentityClient(identity), Scheme: k8sClient.Scheme()}
			requests := reconciler.workloadIdentitiesForServiceAccount(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
			}})

			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: identityKey}))
		})

		It("enqueues operator-managed ServiceAccounts by owner UID label", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Spec.ServiceAccount.Name = "renamed-sa"
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			Expect(k8sClient.Get(ctx, identityKey, identity)).To(Succeed())

			reconciler := &WorkloadIdentityReconciler{Client: indexedWorkloadIdentityClient(identity), Scheme: k8sClient.Scheme()}
			requests := reconciler.workloadIdentitiesForServiceAccount(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountManagedBy: serviceAccountManagerName,
					serviceAccountUID:       string(identity.UID),
					serviceAccountCreatedBy: trueValue,
				},
			}})

			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: identityKey}))
		})

		It("ignores unrelated ServiceAccounts", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			reconciler := &WorkloadIdentityReconciler{Client: indexedWorkloadIdentityClient(identity), Scheme: k8sClient.Scheme()}
			requests := reconciler.workloadIdentitiesForServiceAccount(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      "unrelated",
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountManagedBy: serviceAccountManagerName,
					serviceAccountUID:       "unknown",
				},
			}})

			Expect(requests).To(BeEmpty())
		})

		It("deletes Azure resources and created ServiceAccount based on persisted provenance", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			persisted := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, persisted)).To(Succeed())
			original := persisted.DeepCopy()
			persisted.Status.Subject = serviceAccountSubject(persisted)
			persisted.Status.ServiceAccountProvenance = workloadidentityv1alpha1.ServiceAccountProvenanceCreated
			Expect(k8sClient.Status().Patch(ctx, persisted, client.MergeFrom(original))).To(Succeed())
			serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountUID: "stale-workload-identity-uid",
				},
			}}
			Expect(k8sClient.Create(ctx, serviceAccount)).To(Succeed())
			Expect(k8sClient.Delete(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.deletes).To(Equal(1))

			deletedServiceAccount := &corev1.ServiceAccount{}
			err = k8sClient.Get(ctx, serviceAccountKey, deletedServiceAccount)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("deletes a created ServiceAccount from guarded labels when provenance was not persisted", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountUID:       string(identity.UID),
					serviceAccountCreatedBy: trueValue,
				},
			}}
			Expect(k8sClient.Create(ctx, serviceAccount)).To(Succeed())
			Expect(k8sClient.Delete(ctx, identity)).To(Succeed())

			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: &fakeWorkloadIdentityManager{}}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			deletedServiceAccount := &corev1.ServiceAccount{}
			err = k8sClient.Get(ctx, serviceAccountKey, deletedServiceAccount)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("retains a ServiceAccount when only the created label matches and provenance was not persisted", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountUID:       "different-workload-identity-uid",
					serviceAccountCreatedBy: trueValue,
				},
			}}
			Expect(k8sClient.Create(ctx, serviceAccount)).To(Succeed())
			Expect(k8sClient.Delete(ctx, identity)).To(Succeed())

			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: &fakeWorkloadIdentityManager{}}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			retainedServiceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, retainedServiceAccount)).To(Succeed())
		})

		It("keeps an adopted ServiceAccount when deletion policy is Delete", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			persisted := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, persisted)).To(Succeed())
			original := persisted.DeepCopy()
			persisted.Status.Subject = serviceAccountSubject(persisted)
			persisted.Status.ServiceAccountProvenance = workloadidentityv1alpha1.ServiceAccountProvenanceAdopted
			Expect(k8sClient.Status().Patch(ctx, persisted, client.MergeFrom(original))).To(Succeed())
			serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountUID:       string(identity.UID),
					serviceAccountCreatedBy: trueValue,
				},
			}}
			Expect(k8sClient.Create(ctx, serviceAccount)).To(Succeed())
			Expect(k8sClient.Delete(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.deletes).To(Equal(1))

			adoptedServiceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, adoptedServiceAccount)).To(Succeed())
		})

		It("preserves Azure parents referenced by another WorkloadIdentity during deletion", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			otherIdentity := validWorkloadIdentity(otherIdentityKey.Name, otherIdentityKey.Namespace)
			otherIdentity.Spec.ServiceAccount.Name = testOtherServiceAccountName
			otherIdentity.Spec.Azure.FederatedIdentityCredentialName = testOtherFederatedCredential
			Expect(k8sClient.Create(ctx, otherIdentity)).To(Succeed())
			Expect(k8sClient.Delete(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.deletes).To(Equal(1))
			Expect(manager.options.PreserveResourceGroup).To(BeTrue())
			Expect(manager.options.PreserveUserAssignedIdentity).To(BeTrue())
			Expect(manager.options.ResourceGroupSuccessorUID).To(Equal(string(otherIdentity.UID)))
			Expect(manager.options.UserAssignedIdentitySuccessorUID).To(Equal(string(otherIdentity.UID)))
		})

		It("preserves only the resource group when another WorkloadIdentity uses a different identity", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			otherIdentity := validWorkloadIdentity(otherIdentityKey.Name, otherIdentityKey.Namespace)
			otherIdentity.Spec.ServiceAccount.Name = testOtherServiceAccountName
			otherIdentity.Spec.Azure.UserAssignedIdentityName = "other-uami"
			otherIdentity.Spec.Azure.FederatedIdentityCredentialName = testOtherFederatedCredential
			Expect(k8sClient.Create(ctx, otherIdentity)).To(Succeed())

			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			options, waitForPeers, err := reconciler.workloadIdentityDeleteOptions(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(waitForPeers).To(BeFalse())
			Expect(options.PreserveResourceGroup).To(BeTrue())
			Expect(options.PreserveUserAssignedIdentity).To(BeFalse())
			Expect(options.ResourceGroupSuccessorUID).To(Equal(string(otherIdentity.UID)))
			Expect(options.UserAssignedIdentitySuccessorUID).To(BeEmpty())
		})

		It("coordinates simultaneous shared-parent deletion through one finalizer", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			otherIdentity := validWorkloadIdentity(otherIdentityKey.Name, otherIdentityKey.Namespace)
			otherIdentity.Finalizers = []string{workloadIdentityFinalizer}
			otherIdentity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			otherIdentity.Spec.ServiceAccount.Name = testOtherServiceAccountName
			otherIdentity.Spec.Azure.FederatedIdentityCredentialName = testOtherFederatedCredential
			Expect(k8sClient.Create(ctx, otherIdentity)).To(Succeed())
			Expect(k8sClient.Delete(ctx, identity)).To(Succeed())
			Expect(k8sClient.Delete(ctx, otherIdentity)).To(Succeed())
			Expect(k8sClient.Get(ctx, identityKey, identity)).To(Succeed())
			Expect(k8sClient.Get(ctx, otherIdentityKey, otherIdentity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			coordinatorResult, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: otherIdentityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(coordinatorResult.RequeueAfter).NotTo(BeZero())
			Expect(manager.deletes).To(Equal(0))

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.deletes).To(Equal(1))
			Expect(manager.options.ResourceGroupSuccessorUID).To(Equal(string(otherIdentity.UID)))
			Expect(manager.options.UserAssignedIdentitySuccessorUID).To(Equal(string(otherIdentity.UID)))
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, identityKey, &workloadidentityv1alpha1.WorkloadIdentity{}))
			}).Should(BeTrue())
			Expect(k8sClient.Get(ctx, otherIdentityKey, &workloadidentityv1alpha1.WorkloadIdentity{})).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: otherIdentityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.deletes).To(Equal(2))
			Expect(manager.options.PreserveResourceGroup).To(BeFalse())
			Expect(manager.options.PreserveUserAssignedIdentity).To(BeFalse())
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, otherIdentityKey, &workloadidentityv1alpha1.WorkloadIdentity{}))
			}).Should(BeTrue())
		})
	})
})

func validWorkloadIdentity(name, namespace string) *workloadidentityv1alpha1.WorkloadIdentity {
	return &workloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadidentityv1alpha1.WorkloadIdentitySpec{
			Azure: workloadidentityv1alpha1.AzureWorkloadIdentityConfig{
				SubscriptionID:                  "00000000-0000-0000-0000-000000000000",
				Location:                        "swedencentral",
				ResourceGroupName:               "rg-wi-test",
				UserAssignedIdentityName:        "uami-test",
				FederatedIdentityCredentialName: "fic-test",
			},
			ServiceAccount: workloadidentityv1alpha1.ServiceAccountReference{Name: testServiceAccountName},
		},
	}
}

func createReadyOIDCIssuer(ctx context.Context, name string) {
	issuer := validOIDCIssuer(name)
	Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
	issuer.Status.IssuerURL = testWorkloadIdentityIssuerURL
	apimeta.SetStatusCondition(&issuer.Status.Conditions, metav1.Condition{
		Type:               string(workloadidentityv1alpha1.OIDCIssuerConditionReady),
		Status:             metav1.ConditionTrue,
		Reason:             "Published",
		Message:            "OIDC issuer documents are published",
		ObservedGeneration: issuer.Generation,
	})
	Expect(k8sClient.Status().Update(ctx, issuer)).To(Succeed())
}

func indexedWorkloadIdentityClient(identities ...*workloadidentityv1alpha1.WorkloadIdentity) client.Client {
	builder := clientfake.NewClientBuilder().WithScheme(k8sClient.Scheme())
	for _, identity := range identities {
		builder = builder.WithObjects(identity)
	}
	return builder.
		WithIndex(&workloadidentityv1alpha1.WorkloadIdentity{}, workloadIdentityServiceAccountNameIndex, func(object client.Object) []string {
			return []string{object.(*workloadidentityv1alpha1.WorkloadIdentity).Spec.ServiceAccount.Name}
		}).
		WithIndex(&workloadidentityv1alpha1.WorkloadIdentity{}, workloadIdentityUIDIndex, func(object client.Object) []string {
			return []string{string(object.GetUID())}
		}).
		Build()
}

func deleteWorkloadIdentity(ctx context.Context, key types.NamespacedName) {
	identity := &workloadidentityv1alpha1.WorkloadIdentity{}
	if err := k8sClient.Get(ctx, key, identity); err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		return
	}

	if len(identity.Finalizers) > 0 {
		patch := client.MergeFrom(identity.DeepCopy())
		identity.Finalizers = nil
		Expect(k8sClient.Patch(ctx, identity, patch)).To(Succeed())
	}

	err := k8sClient.Delete(ctx, identity)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), fmt.Sprintf("unexpected delete error: %v", err))
	}
}

func deleteServiceAccount(ctx context.Context, key types.NamespacedName) {
	serviceAccount := &corev1.ServiceAccount{}
	if err := k8sClient.Get(ctx, key, serviceAccount); err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		return
	}
	err := k8sClient.Delete(ctx, serviceAccount)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), fmt.Sprintf("unexpected delete error: %v", err))
	}
}
