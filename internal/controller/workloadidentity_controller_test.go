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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/workloadidentity"
)

const (
	testWorkloadIdentityIssuerURL = "https://oidctest123.blob.core.windows.net/oidc"
	testWorkloadNamespace         = "default"
)

type fakeWorkloadIdentityManager struct {
	managed workloadidentity.ManagedIdentity
	err     error
	ensures int
	deletes int
	subject string
}

func (f *fakeWorkloadIdentityManager) Ensure(_ context.Context, _ *workloadidentityv1alpha1.WorkloadIdentity, _, subject string) (workloadidentity.ManagedIdentity, error) {
	f.ensures++
	f.subject = subject
	return f.managed, f.err
}

func (f *fakeWorkloadIdentityManager) Delete(context.Context, *workloadidentityv1alpha1.WorkloadIdentity) error {
	f.deletes++
	return f.err
}

var _ = Describe("WorkloadIdentity Controller", func() {
	Context("When reconciling a resource", func() {
		ctx := context.Background()
		identityKey := types.NamespacedName{Name: "test-workload", Namespace: testWorkloadNamespace}
		otherIdentityKey := types.NamespacedName{Name: "other-workload", Namespace: testWorkloadNamespace}
		issuerKey := types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}
		serviceAccountKey := types.NamespacedName{Name: "test-sa", Namespace: testWorkloadNamespace}

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
				ClientID:    "client-id",
				PrincipalID: "principal-id",
				TenantID:    "tenant-id",
				AzureResources: []workloadidentityv1alpha1.AzureResource{{
					ID:   "/subscriptions/test/resourceGroups/rg-wi-test/providers/Microsoft.ManagedIdentity/userAssignedIdentities/uami-test",
					Kind: "UserAssignedIdentity",
				}},
			}}

			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			Expect(manager.ensures).To(Equal(1))
			Expect(manager.subject).To(Equal("system:serviceaccount:default:test-sa"))

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccount)).To(Succeed())
			Expect(serviceAccount.Labels[serviceAccountUseLabel]).To(Equal(trueValue))
			Expect(serviceAccount.Labels[serviceAccountCreatedBy]).To(Equal(trueValue))
			Expect(serviceAccount.Annotations[serviceAccountClientID]).To(Equal("client-id"))
			Expect(serviceAccount.Annotations[serviceAccountTenantID]).To(Equal("tenant-id"))

			updated := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, identityKey, updated)).To(Succeed())
			Expect(updated.Status.ClientID).To(Equal("client-id"))
			Expect(updated.Status.Subject).To(Equal("system:serviceaccount:default:test-sa"))
			condition := apimeta.FindStatusCondition(updated.Status.Conditions, string(workloadidentityv1alpha1.WorkloadIdentityConditionReady))
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		})

		It("adopts an existing ServiceAccount without marking it as created", func() {
			createReadyOIDCIssuer(ctx, issuerKey.Name)
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels:    map[string]string{"existing": "true"},
			}})).To(Succeed())

			manager := &fakeWorkloadIdentityManager{managed: workloadidentity.ManagedIdentity{ClientID: "client-id"}}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, serviceAccountKey, serviceAccount)).To(Succeed())
			Expect(serviceAccount.Labels["existing"]).To(Equal(trueValue))
			Expect(serviceAccount.Labels[serviceAccountUseLabel]).To(Equal(trueValue))
			Expect(serviceAccount.Labels[serviceAccountCreatedBy]).To(Equal("false"))
			Expect(serviceAccount.Annotations[serviceAccountClientID]).To(Equal("client-id"))
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

			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			requests := reconciler.workloadIdentitiesForOIDCIssuer(ctx, &workloadidentityv1alpha1.OIDCIssuer{ObjectMeta: metav1.ObjectMeta{Name: issuerKey.Name}})

			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: identityKey}, reconcile.Request{NamespacedName: otherIdentityKey}))
			Expect(reconciler.workloadIdentitiesForOIDCIssuer(ctx, &workloadidentityv1alpha1.OIDCIssuer{ObjectMeta: metav1.ObjectMeta{Name: "other"}})).To(BeNil())
		})

		It("deletes Azure resources and created ServiceAccount when deletion policy is Delete", func() {
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

			manager := &fakeWorkloadIdentityManager{}
			reconciler := &WorkloadIdentityReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Manager: manager}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: identityKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.deletes).To(Equal(1))

			deletedServiceAccount := &corev1.ServiceAccount{}
			err = k8sClient.Get(ctx, serviceAccountKey, deletedServiceAccount)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("keeps an adopted ServiceAccount when deletion policy is Delete", func() {
			identity := validWorkloadIdentity(identityKey.Name, identityKey.Namespace)
			identity.Finalizers = []string{workloadIdentityFinalizer}
			identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountKey.Name,
				Namespace: serviceAccountKey.Namespace,
				Labels: map[string]string{
					serviceAccountUID:       string(identity.UID),
					serviceAccountCreatedBy: "false",
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
			ServiceAccount: workloadidentityv1alpha1.ServiceAccountReference{Name: "test-sa"},
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
