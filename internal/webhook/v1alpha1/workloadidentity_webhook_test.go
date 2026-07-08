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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

const (
	webhookSubscriptionID                = "00000000-0000-0000-0000-000000000000"
	otherFederatedIdentityCredentialName = "fic-other-test"
	otherServiceAccountName              = "other-service-account"
)

var _ = Describe("WorkloadIdentity Webhook", func() {
	const (
		identityName          = "test-workload"
		duplicateIdentityName = "duplicate-workload"
	)

	BeforeEach(func() {
		ensureWebhookNamespace("other")
		deleteWebhookWorkloadIdentity(identityName, "default")
		deleteWebhookWorkloadIdentity(duplicateIdentityName, "default")
		deleteWebhookWorkloadIdentity(duplicateIdentityName, "other")
	})

	AfterEach(func() {
		deleteWebhookWorkloadIdentity(identityName, "default")
		deleteWebhookWorkloadIdentity(duplicateIdentityName, "default")
		deleteWebhookWorkloadIdentity(duplicateIdentityName, "other")
	})

	Context("When creating WorkloadIdentity", func() {
		It("allows a unique ServiceAccount reference and Azure federated credential tuple", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")

			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
		})

		It("denies duplicate ServiceAccount references in the same namespace", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, "default")
			duplicate.Spec.Azure.FederatedIdentityCredentialName = otherFederatedIdentityCredentialName

			err := k8sClient.Create(ctx, duplicate)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.serviceAccount.name"))
			Expect(err.Error()).To(ContainSubstring("already referenced by WorkloadIdentity default/test-workload"))
		})

		It("allows the same ServiceAccount name in different namespaces", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, "other")
			duplicate.Spec.Azure.FederatedIdentityCredentialName = otherFederatedIdentityCredentialName

			Expect(k8sClient.Create(ctx, duplicate)).To(Succeed())
		})

		It("denies duplicate Azure federated credential tuples", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, "default")
			duplicate.Spec.ServiceAccount.Name = otherServiceAccountName

			err := k8sClient.Create(ctx, duplicate)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.azure.federatedIdentityCredentialName"))
			Expect(err.Error()).To(ContainSubstring("Azure federated identity credential tuple already referenced"))
		})

		It("denies duplicate Azure federated credential tuples across namespaces", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, "other")

			err := k8sClient.Create(ctx, duplicate)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.azure.federatedIdentityCredentialName"))
			Expect(err.Error()).To(ContainSubstring("Azure federated identity credential tuple already referenced"))
		})

		It("denies duplicate Azure federated credential tuples case-insensitively", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, "default")
			duplicate.Spec.ServiceAccount.Name = otherServiceAccountName
			duplicate.Spec.Azure.SubscriptionID = webhookSubscriptionID
			duplicate.Spec.Azure.ResourceGroupName = "RG-WI-TEST"
			duplicate.Spec.Azure.UserAssignedIdentityName = "UAMI-WI-TEST"
			duplicate.Spec.Azure.FederatedIdentityCredentialName = "FIC-WI-TEST"

			err := k8sClient.Create(ctx, duplicate)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.azure.federatedIdentityCredentialName"))
			Expect(err.Error()).To(ContainSubstring("Azure federated identity credential tuple already referenced"))
		})

		It("denies invalid ServiceAccount names through CRD schema validation", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")
			identity.Spec.ServiceAccount.Name = "Invalid_Service_Account"

			err := k8sClient.Create(ctx, identity)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.serviceAccount.name"))
		})
	})

	Context("When updating WorkloadIdentity", func() {
		It("allows updating the same WorkloadIdentity without reporting itself as a duplicate", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: identityName, Namespace: "default"}, current)).To(Succeed())
			current.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete

			Expect(k8sClient.Update(ctx, current)).To(Succeed())
		})

		It("denies updates that would duplicate another ServiceAccount reference", func() {
			identity := validWebhookWorkloadIdentity(identityName, "default")
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			other := validWebhookWorkloadIdentity(duplicateIdentityName, "default")
			other.Spec.ServiceAccount.Name = otherServiceAccountName
			other.Spec.Azure.FederatedIdentityCredentialName = otherFederatedIdentityCredentialName
			Expect(k8sClient.Create(ctx, other)).To(Succeed())

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: duplicateIdentityName, Namespace: "default"}, current)).To(Succeed())
			current.Spec.ServiceAccount.Name = identity.Spec.ServiceAccount.Name

			err := k8sClient.Update(ctx, current)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("already referenced by WorkloadIdentity default/test-workload"))
		})
	})
})

func validWebhookWorkloadIdentity(name, namespace string) *workloadidentityv1alpha1.WorkloadIdentity {
	return &workloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadidentityv1alpha1.WorkloadIdentitySpec{
			Azure: workloadidentityv1alpha1.AzureWorkloadIdentityConfig{
				SubscriptionID:                  webhookSubscriptionID,
				Location:                        "swedencentral",
				ResourceGroupName:               "rg-wi-test",
				UserAssignedIdentityName:        "uami-wi-test",
				FederatedIdentityCredentialName: "fic-wi-test",
			},
			ServiceAccount: workloadidentityv1alpha1.ServiceAccountReference{Name: "test-service-account"},
		},
	}
}

func ensureWebhookNamespace(name string) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Create(ctx, namespace)
	if err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue(), "unexpected Namespace create error: %v", err)
	}
}
