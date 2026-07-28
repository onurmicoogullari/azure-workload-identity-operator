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
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

const (
	otherFederatedIdentityCredentialName = "fic-other-test"
	otherServiceAccountName              = "other-service-account"
	defaultWebhookNamespace              = "default"
)

var _ = Describe("WorkloadIdentity Webhook", func() {
	const (
		identityName          = "test-workload"
		duplicateIdentityName = "duplicate-workload"
	)

	BeforeEach(func() {
		ensureWebhookNamespace("other")
		ensureWebhookNamespace("team")
		ensureWebhookNamespace("team-app")
		deleteWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
		deleteWebhookWorkloadIdentity(duplicateIdentityName, defaultWebhookNamespace)
		deleteWebhookWorkloadIdentity(duplicateIdentityName, "other")
		deleteWebhookWorkloadIdentity(identityName, "team")
		deleteWebhookWorkloadIdentity(duplicateIdentityName, "team-app")
	})

	AfterEach(func() {
		deleteWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
		deleteWebhookWorkloadIdentity(duplicateIdentityName, defaultWebhookNamespace)
		deleteWebhookWorkloadIdentity(duplicateIdentityName, "other")
		deleteWebhookWorkloadIdentity(identityName, "team")
		deleteWebhookWorkloadIdentity(duplicateIdentityName, "team-app")
	})

	Context("When creating WorkloadIdentity", func() {
		It("allows a unique ServiceAccount reference and Azure federated credential tuple", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)

			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
		})

		It("denies duplicate ServiceAccount references in the same namespace", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, defaultWebhookNamespace)
			duplicate.Spec.Azure.FederatedIdentityCredentialName = otherFederatedIdentityCredentialName

			err := k8sClient.Create(ctx, duplicate)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.serviceAccount.name"))
			Expect(err.Error()).To(ContainSubstring("already referenced by WorkloadIdentity default/test-workload"))
		})

		It("allows the same ServiceAccount name in different namespaces", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, "other")
			duplicate.Spec.Azure.FederatedIdentityCredentialName = otherFederatedIdentityCredentialName

			Expect(k8sClient.Create(ctx, duplicate)).To(Succeed())
		})

		It("denies duplicate resolved user assigned identity names", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, defaultWebhookNamespace)
			duplicate.Spec.ServiceAccount.Name = otherServiceAccountName

			err := k8sClient.Create(ctx, duplicate)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.azure.userAssignedIdentityName"))
			Expect(err.Error()).To(ContainSubstring("resolved Azure user assigned identity name"))
		})

		It("denies colliding resolved user assigned identity names across namespaces", func() {
			identity := validWebhookWorkloadIdentity(identityName, "team")
			identity.Spec.Azure.UserAssignedIdentityName = "app-identity"
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, "team-app")
			duplicate.Spec.Azure.UserAssignedIdentityName = "identity"

			err := k8sClient.Create(ctx, duplicate)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring(`"team-app-identity"`))
		})

		It("denies duplicate resolved user assigned identity names case-insensitively", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, defaultWebhookNamespace)
			duplicate.Spec.ServiceAccount.Name = otherServiceAccountName
			duplicate.Spec.Azure.UserAssignedIdentityName = "UAMI-WI-TEST"

			err := k8sClient.Create(ctx, duplicate)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.azure.userAssignedIdentityName"))
		})

		It("allows the same federated credential name under distinct identities", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())
			duplicate := validWebhookWorkloadIdentity(duplicateIdentityName, defaultWebhookNamespace)
			duplicate.Spec.ServiceAccount.Name = otherServiceAccountName
			duplicate.Spec.Azure.UserAssignedIdentityName = "other-identity"

			Expect(k8sClient.Create(ctx, duplicate)).To(Succeed())
		})

		It("denies a suffix whose resolved identity name exceeds Azure length limits", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			identity.Spec.Azure.UserAssignedIdentityName = strings.Repeat("a", 121)

			err := k8sClient.Create(ctx, identity)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("resolved user assigned identity name"))
		})

		It("denies invalid ServiceAccount names through CRD schema validation", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			identity.Spec.ServiceAccount.Name = "Invalid_Service_Account"

			err := k8sClient.Create(ctx, identity)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.serviceAccount.name"))
		})
	})

	Context("When updating WorkloadIdentity", func() {
		It("allows updating the same WorkloadIdentity without reporting itself as a duplicate", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: identityName, Namespace: defaultWebhookNamespace}, current)).To(Succeed())
			current.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete

			Expect(k8sClient.Update(ctx, current)).To(Succeed())
		})

		It("denies changing the ServiceAccount name through CRD schema validation", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(identity), current)).To(Succeed())
			current.Spec.ServiceAccount.Name = otherServiceAccountName

			err := k8sClient.Update(ctx, current)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("field is immutable"))
		})

		It("denies changing the ServiceAccount name through programmatic validation", func() {
			oldIdentity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			newIdentity := oldIdentity.DeepCopy()
			newIdentity.Spec.ServiceAccount.Name = otherServiceAccountName
			validator := &WorkloadIdentityValidator{Client: k8sClient}

			_, err := validator.ValidateUpdate(ctx, oldIdentity, newIdentity)

			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.serviceAccount.name"))
			Expect(err.Error()).To(ContainSubstring("field is immutable"))
		})

		It("denies changing the user assigned identity suffix", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(identity), current)).To(Succeed())
			current.Spec.Azure.UserAssignedIdentityName = "different"

			err := k8sClient.Update(ctx, current)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("field is immutable"))
		})

		It("denies changing the federated credential name", func() {
			identity := validWebhookWorkloadIdentity(identityName, defaultWebhookNamespace)
			Expect(k8sClient.Create(ctx, identity)).To(Succeed())

			current := &workloadidentityv1alpha1.WorkloadIdentity{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(identity), current)).To(Succeed())
			current.Spec.Azure.FederatedIdentityCredentialName = "different"

			err := k8sClient.Update(ctx, current)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("field is immutable"))
		})

	})
})

func validWebhookWorkloadIdentity(name, namespace string) *workloadidentityv1alpha1.WorkloadIdentity {
	return &workloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadidentityv1alpha1.WorkloadIdentitySpec{
			Azure: workloadidentityv1alpha1.AzureWorkloadIdentityConfig{
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
