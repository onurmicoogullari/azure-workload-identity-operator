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
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	testWorkloadIdentityIssuerURL   = "https://oidctest123.blob.core.windows.net/oidc"
	testWorkloadNamespace           = "default"
	testServiceAccountName          = "test-sa"
	testOtherServiceAccountName     = "other-sa"
	testClientID                    = "client-id"
	testTenantID                    = "tenant-id"
	testExistingLabel               = "existing"
	testPreviousWorkloadIdentityUID = "previous-workload-identity-uid"
)

type fakeWorkloadIdentityManager struct {
	managed  workloadidentity.ManagedIdentity
	err      error
	onEnsure func()
	ensures  int
	deletes  int
	subject  string
}

type fakeRecoveryDetector struct {
	evidence workloadidentity.RecoveryRequiredEvidence
	err      error
	calls    int
}

func (f *fakeRecoveryDetector) DetectRecovery(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentity,
) (workloadidentity.RecoveryRequiredEvidence, error) {
	f.calls++
	return f.evidence, f.err
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

	Describe("ServiceAccount provenance", func() {
		It("does not accept provenance without a recorded ServiceAccount subject", func() {
			identity := validWorkloadIdentity("test-workload", testWorkloadNamespace)
			identity.Status.ServiceAccountProvenance = workloadidentityv1alpha1.ServiceAccountProvenanceCreated

			_, recorded := persistedServiceAccountProvenance(identity)

			Expect(recorded).To(BeFalse())
		})

	})

	Describe("CRD immutability without webhooks", func() {
		It("rejects changes to Azure names and the ServiceAccount name", func() {
			ctx := context.Background()
			key := types.NamespacedName{Name: "cel-immutability", Namespace: testWorkloadNamespace}
			identity := validWorkloadIdentity(key.Name, key.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			DeferCleanup(func() {
				deleteWorkloadIdentity(ctx, key)
			})

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
			current.Spec.Azure.UserAssignedIdentityName = "different"
			err := k8sClient.Update(ctx, current)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("field is immutable"))

			Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
			current.Spec.Azure.FederatedIdentityCredentialName = "different"
			err = k8sClient.Update(ctx, current)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("field is immutable"))

			Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
			current.Spec.ServiceAccount.Name = testOtherServiceAccountName
			err = k8sClient.Update(ctx, current)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("field is immutable"))
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
					ID:   "/subscriptions/test/resourceGroups/rg-wi-test/providers/Microsoft.ManagedIdentity/userAssignedIdentities/default-uami-test",
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

		It("preserves logical provenance when the ServiceAccount is replaced after inspection", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: testClientID}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			originalServiceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, originalServiceAccount)).To(Succeed())
			manager.onEnsure = func() {
				manager.onEnsure = nil
				Expect(k8sClient.Delete(ctx, originalServiceAccount)).To(Succeed())
				Eventually(func() bool {
					return apierrors.IsNotFound(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{}))
				}).Should(BeTrue())
				Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
					Name:      serviceAccountKey.Name,
					Namespace: serviceAccountKey.Namespace,
					Labels:    map[string]string{testExistingLabel: trueValue},
				}})).To(Succeed())
			}

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			replacement := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, replacement)).To(Succeed())
			Expect(replacement.UID).NotTo(Equal(originalServiceAccount.UID))
			Expect(replacement.Labels[testExistingLabel]).To(Equal(trueValue))
			Expect(replacement.Labels[serviceAccountCreatedBy]).To(Equal(trueValue))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ServiceAccountUID).To(Equal(string(replacement.UID)))
			Expect(updated.Status.ServiceAccountProvenance).To(Equal(workloadidentityv1alpha1.ServiceAccountProvenanceCreated))
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

		It("publishes recovery evidence when Azure and the ServiceAccount have the same previous owner", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountManagedBy: serviceAccountManagerName,
					serviceAccountUID:       testPreviousWorkloadIdentityUID,
				},
			}})).To(Succeed())

			manager := &fakeWorkloadIdentityManager{}
			detector := &fakeRecoveryDetector{evidence: workloadidentity.RecoveryRequiredEvidence{
				PreviousWorkloadIdentityUID: testPreviousWorkloadIdentityUID,
			}}
			reconciler := &WorkloadIdentityReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				Manager:          manager,
				RecoveryDetector: detector,
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.ensures).To(Equal(0))
			Expect(detector.calls).To(Equal(1))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(
				updated.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal(workloadidentity.ReasonRecoveryRequired))
			Expect(updated.Status.Recovery).NotTo(BeNil())
			Expect(updated.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(types.UID(testPreviousWorkloadIdentityUID)))
		})

		It("keeps recovery evidence while a fenced recovery is in progress", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			identity.Status.Recovery = &workloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
				PreviousWorkloadIdentityUID: testPreviousWorkloadIdentityUID,
			}
			Expect(k8sClient.Status().Update(ctx, identity)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountManagedBy: serviceAccountManagerName,
					serviceAccountUID:       testPreviousWorkloadIdentityUID,
				},
			}})).To(Succeed())

			manager := &fakeWorkloadIdentityManager{}
			detector := &fakeRecoveryDetector{err: workloadidentity.NewConflictError(
				workloadidentity.ReasonRecoveryInProgress,
				"controlled recovery is fenced",
			)}
			reconciler := &WorkloadIdentityReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				Manager:          manager,
				RecoveryDetector: detector,
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(workloadIdentityRecoveryPollInterval))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(
				updated.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal(workloadidentity.ReasonRecoveryInProgress))
			Expect(updated.Status.Recovery).NotTo(BeNil())
			Expect(updated.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(types.UID(testPreviousWorkloadIdentityUID)))
		})

		It("rejects a stale normal status patch after recovery acquires the target lock", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			persisted := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, persisted)).To(Succeed())
			persisted.Status.Recovery = &workloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
				PreviousWorkloadIdentityUID: testPreviousWorkloadIdentityUID,
			}
			apimeta.SetStatusCondition(&persisted.Status.Conditions, metav1.Condition{
				Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				Status:             metav1.ConditionFalse,
				Reason:             workloadidentity.ReasonRecoveryRequired,
				Message:            "recovery required",
				ObservedGeneration: persisted.Generation,
			})
			Expect(k8sClient.Status().Update(ctx, persisted)).To(Succeed())

			stale := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, stale)).To(Succeed())
			locked := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, locked)).To(Succeed())
			apimeta.SetStatusCondition(&locked.Status.Conditions, metav1.Condition{
				Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				Status:             metav1.ConditionFalse,
				Reason:             workloadidentity.ReasonRecoveryInProgress,
				Message:            "controlled recovery is in progress",
				ObservedGeneration: locked.Generation,
			})
			Expect(k8sClient.Status().Update(ctx, locked)).To(Succeed())

			reconciler := &WorkloadIdentityReconciler{Client: k8sClient}
			err := reconciler.setWorkloadIdentityRecoveryRequired(
				ctx,
				stale,
				workloadidentity.RecoveryRequiredEvidence{
					PreviousWorkloadIdentityUID: testPreviousWorkloadIdentityUID,
				},
				"stale normal reconciliation",
			)
			Expect(apierrors.IsConflict(err)).To(BeTrue())

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			condition := apimeta.FindStatusCondition(
				current.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal(workloadidentity.ReasonRecoveryInProgress))
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

		It("does not write a ServiceAccount when Azure recovery is required", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{
				err: workloadidentity.NewRecoveryRequiredError(
					"an earlier WorkloadIdentity instance owns the user assigned identity",
					types.UID("earlier-workload-identity-uid"),
				),
			}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(reconciler.refreshInterval(identity)))
			Expect(manager.ensures).To(Equal(1))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{}))).To(BeTrue())

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(workloadidentity.ReasonRecoveryRequired))
			Expect(updated.Status.Recovery).NotTo(BeNil())
			Expect(updated.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(types.UID("earlier-workload-identity-uid")))
		})

		It("polls while Azure recovery is in progress before the ServiceAccount exists", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			identity.Status.Recovery = &workloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
				PreviousWorkloadIdentityUID: testPreviousWorkloadIdentityUID,
			}
			Expect(k8sClient.Status().Update(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{err: workloadidentity.NewConflictError(
				workloadidentity.ReasonRecoveryInProgress,
				"controlled recovery is fenced",
			)}
			reconciler := &WorkloadIdentityReconciler{
				Client:  k8sClient,
				Scheme:  k8sClient.Scheme(),
				Manager: manager,
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(workloadIdentityRecoveryPollInterval))
			Expect(manager.ensures).To(Equal(1))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{}))).To(BeTrue())

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			condition := apimeta.FindStatusCondition(
				updated.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal(workloadidentity.ReasonRecoveryInProgress))
			Expect(updated.Status.Recovery).NotTo(BeNil())
			Expect(updated.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(types.UID(testPreviousWorkloadIdentityUID)))
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

		DescribeTable("preserves active recovery state while OIDC is unavailable and resumes when ready",
			func(recoveryReason string, issuerExists bool) {
				if issuerExists {
					Expect(k8sClient.Create(ctx, validOIDCIssuer(issuerKey.Name))).To(Succeed())
				}
				identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
				Expect(k8sClient.Create(ctx, identity)).To(Succeed())
				identity.Status.Recovery = &workloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
					PreviousWorkloadIdentityUID: testPreviousWorkloadIdentityUID,
				}
				apimeta.SetStatusCondition(&identity.Status.Conditions, metav1.Condition{
					Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
					Status:             metav1.ConditionFalse,
					Reason:             recoveryReason,
					ObservedGeneration: identity.Generation,
				})
				Expect(k8sClient.Status().Update(ctx, identity)).To(Succeed())

				manager := &fakeWorkloadIdentityManager{}
				if recoveryReason == workloadidentity.ReasonRecoveryRequired {
					manager.err = workloadidentity.NewRecoveryRequiredError(
						"controlled recovery is still required",
						testPreviousWorkloadIdentityUID,
					)
				} else {
					manager.err = workloadidentity.NewConflictError(
						workloadidentity.ReasonRecoveryInProgress,
						"controlled recovery is fenced",
					)
				}
				reconciler := &WorkloadIdentityReconciler{
					Client:  k8sClient,
					Scheme:  k8sClient.Scheme(),
					Manager: manager,
				}

				result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
				Expect(err).NotTo(HaveOccurred())
				expectedRequeue := 30 * time.Second
				if recoveryReason == workloadidentity.ReasonRecoveryInProgress {
					expectedRequeue = workloadIdentityRecoveryPollInterval
				}
				Expect(result.RequeueAfter).To(Equal(expectedRequeue))
				Expect(manager.ensures).To(Equal(0))

				current := &workloadidentityv1alpha1.WorkloadIdentity{}
				Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
				ready := apimeta.FindStatusCondition(
					current.Status.Conditions,
					string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				)
				Expect(ready).NotTo(BeNil())
				Expect(ready.Reason).To(Equal(recoveryReason))
				Expect(current.Status.Recovery).NotTo(BeNil())
				Expect(current.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(types.UID(testPreviousWorkloadIdentityUID)))

				if issuerExists {
					issuer := &workloadidentityv1alpha1.OIDCIssuer{}
					Expect(k8sClient.Get(ctx, issuerKey, issuer)).To(Succeed())
					issuer.Status.IssuerURL = testWorkloadIdentityIssuerURL
					apimeta.SetStatusCondition(&issuer.Status.Conditions, metav1.Condition{
						Type:               string(workloadidentityv1alpha1.OIDCIssuerConditionReady),
						Status:             metav1.ConditionTrue,
						Reason:             "RecoveryIssuerReady",
						ObservedGeneration: issuer.Generation,
					})
					Expect(k8sClient.Status().Update(ctx, issuer)).To(Succeed())
				} else {
					createReadyOIDCIssuer(ctx, issuerKey.Name)
				}

				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
				Expect(err).NotTo(HaveOccurred())
				expectedEnsures := 1
				if recoveryReason == workloadidentity.ReasonRecoveryInProgress {
					expectedEnsures = 0
				}
				Expect(manager.ensures).To(Equal(expectedEnsures))
				Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
				ready = apimeta.FindStatusCondition(
					current.Status.Conditions,
					string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				)
				Expect(ready).NotTo(BeNil())
				Expect(ready.Reason).To(Equal(recoveryReason))
				Expect(current.Status.Recovery).NotTo(BeNil())
				Expect(current.Status.Recovery.PreviousWorkloadIdentityUID).To(Equal(types.UID(testPreviousWorkloadIdentityUID)))

				if recoveryReason == workloadidentity.ReasonRecoveryInProgress {
					apimeta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
						Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
						Status:             metav1.ConditionFalse,
						Reason:             recoveryReasonCompleted,
						Message:            "Controlled recovery completed; normal reconciliation will resume",
						ObservedGeneration: current.Generation,
					})
					Expect(k8sClient.Status().Update(ctx, current)).To(Succeed())
					manager.err = nil

					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
					Expect(err).NotTo(HaveOccurred())
					Expect(manager.ensures).To(Equal(1))
					Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
					ready = apimeta.FindStatusCondition(
						current.Status.Conditions,
						string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
					)
					Expect(ready).NotTo(BeNil())
					Expect(ready.Status).To(Equal(metav1.ConditionTrue))
					Expect(current.Status.Recovery).To(BeNil())
				}
			},
			Entry("RecoveryRequired across an unready issuer", workloadidentity.ReasonRecoveryRequired, true),
			Entry("RecoveryInProgress across a missing issuer", workloadidentity.ReasonRecoveryInProgress, false),
		)

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

		It("holds deletionPolicy Delete while recovery is in progress and releases it afterward", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			Expect(k8sClient.Get(ctx, identityKey, identity)).To(Succeed())
			identity.Status.Recovery = &workloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
				PreviousWorkloadIdentityUID: testPreviousWorkloadIdentityUID,
			}
			apimeta.SetStatusCondition(&identity.Status.Conditions, metav1.Condition{
				Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				Status:             metav1.ConditionFalse,
				Reason:             workloadidentity.ReasonRecoveryInProgress,
				ObservedGeneration: identity.Generation,
			})
			Expect(k8sClient.Status().Update(ctx, identity)).To(Succeed())
			Expect(k8sClient.Delete(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{}
			reconciler := &WorkloadIdentityReconciler{
				Client:  k8sClient,
				Scheme:  k8sClient.Scheme(),
				Manager: manager,
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(workloadIdentityRecoveryPollInterval))
			Expect(manager.deletes).To(Equal(0))

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, current)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(current, workloadIdentityFinalizer)).To(BeTrue())
			original := current.DeepCopy()
			apimeta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
				Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				Status:             metav1.ConditionFalse,
				Reason:             recoveryReasonCompleted,
				ObservedGeneration: current.Generation,
			})
			Expect(k8sClient.Status().Patch(ctx, current, client.MergeFrom(original))).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.deletes).To(Equal(1))
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(
					ctx,
					identityKey,
					&workloadidentityv1alpha1.WorkloadIdentity{},
				))
			}).Should(BeTrue())
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

		It("retains Azure resources and the ServiceAccount when deletion policy is Retain", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountManagedBy: serviceAccountManagerName,
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
			Expect(manager.deletes).To(Equal(0))
			Expect(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{})).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, identityKey, &workloadidentityv1alpha1.WorkloadIdentity{}))
			}).Should(BeTrue())
		})

		It("keeps the finalizer and ServiceAccount on an Azure ownership conflict during deletion", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountManagedBy: serviceAccountManagerName,
					serviceAccountUID:       string(identity.UID),
					serviceAccountCreatedBy: trueValue,
				},
			}}
			Expect(k8sClient.Create(ctx, serviceAccount)).To(Succeed())
			Expect(k8sClient.Delete(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{err: workloadidentity.NewConflictError(
				workloadidentity.ReasonAzureResourceOwnershipConflict,
				"foreign identity owns Azure resources",
			)}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).To(MatchError(ContainSubstring("foreign identity")))
			Expect(manager.deletes).To(Equal(1))

			terminating := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, terminating)).To(Succeed())
			Expect(terminating.Finalizers).To(ContainElement(workloadIdentityFinalizer))
			Expect(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{})).To(Succeed())
		})

		It("reports recovery without writes, then allows deletion while preserving resources", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			manager := &fakeWorkloadIdentityManager{err: workloadidentity.NewRecoveryRequiredError(
				"old UID owns Azure resources",
				types.UID("old-uid"),
			)}
			recorder := events.NewFakeRecorder(1)
			reconciler := &WorkloadIdentityReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Manager:  manager,
				Recorder: recorder,
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(reconciler.refreshInterval(identity)))
			Expect(manager.ensures).To(Equal(1))
			Expect(manager.deletes).To(Equal(0))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{}))).To(BeTrue())

			recovering := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, recovering)).To(Succeed())
			condition := apimeta.FindStatusCondition(
				recovering.Status.Conditions,
				string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
			)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(workloadidentity.ReasonRecoveryRequired))
			Expect(k8sClient.Delete(ctx, recovering)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.deletes).To(Equal(1))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, serviceAccountKey, &corev1.ServiceAccount{}))).To(BeTrue())
			Eventually(recorder.Events).Should(Receive(ContainSubstring("Warning RecoveryRequired")))
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, identityKey, &workloadidentityv1alpha1.WorkloadIdentity{}))
			}).Should(BeTrue())
		})
	})
})

func validWorkloadIdentity(name, namespace string) *workloadidentityv1alpha1.WorkloadIdentity {
	return &workloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadidentityv1alpha1.WorkloadIdentitySpec{
			Azure: workloadidentityv1alpha1.AzureWorkloadIdentityConfig{
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
