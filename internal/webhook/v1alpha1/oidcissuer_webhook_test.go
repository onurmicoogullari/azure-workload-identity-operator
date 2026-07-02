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
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

var _ = Describe("OIDCIssuer Webhook", func() {
	const blockingIdentityName = "blocking-workload"
	const webhookIssuerURL = "https://oidctest123.blob.core.windows.net/oidc"

	BeforeEach(func() {
		deleteWebhookWorkloadIdentity(blockingIdentityName, "default")
		deleteWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
		deleteWebhookOIDCIssuer("not-default")
	})

	AfterEach(func() {
		deleteWebhookWorkloadIdentity(blockingIdentityName, "default")
		deleteWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
		deleteWebhookOIDCIssuer("not-default")
	})

	Context("When deleting OIDCIssuer", func() {
		It("denies deleting the singleton while WorkloadIdentities exist", func() {
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
			Expect(k8sClient.Create(ctx, validWebhookWorkloadIdentity(blockingIdentityName, "default"))).To(Succeed())

			err := k8sClient.Delete(ctx, issuer)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("OIDCIssuer deletion is blocked"))
			Expect(err.Error()).To(ContainSubstring("default/blocking-workload"))

			current := &workloadidentityv1alpha1.OIDCIssuer{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: workloadidentityv1alpha1.OIDCIssuerName}, current)).To(Succeed())
			Expect(current.DeletionTimestamp.IsZero()).To(BeTrue())
		})

		It("allows deleting the singleton when no WorkloadIdentities exist", func() {
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			Expect(k8sClient.Create(ctx, issuer)).To(Succeed())

			Expect(k8sClient.Delete(ctx, issuer)).To(Succeed())

			Eventually(func(g Gomega) {
				current := &workloadidentityv1alpha1.OIDCIssuer{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: workloadidentityv1alpha1.OIDCIssuerName}, current)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}).Should(Succeed())
		})

		It("allows deleting unsupported OIDCIssuers", func() {
			issuer := validWebhookOIDCIssuer("not-default")
			Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
			Expect(k8sClient.Create(ctx, validWebhookWorkloadIdentity(blockingIdentityName, "default"))).To(Succeed())

			Expect(k8sClient.Delete(ctx, issuer)).To(Succeed())
		})

		It("denies deleting the singleton while the cluster still mints service account tokens with the issuer URL", func() {
			serviceAccountTokens := &fakeServiceAccountTokenClient{currentIssuer: webhookIssuerURL}
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Status.IssuerURL = webhookIssuerURL
			validator := &OIDCIssuerValidator{
				Client:               k8sClient,
				ServiceAccountTokens: serviceAccountTokens,
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("still minting service account tokens"))
			Expect(err.Error()).To(ContainSubstring(webhookIssuerURL))
			Expect(serviceAccountTokens.gets).To(Equal(1))
		})

		It("allows deleting the singleton after the cluster service account token issuer handoff", func() {
			serviceAccountTokens := &fakeServiceAccountTokenClient{currentIssuer: "https://issuer.example"}
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Status.IssuerURL = webhookIssuerURL
			validator := &OIDCIssuerValidator{
				Client:               k8sClient,
				ServiceAccountTokens: serviceAccountTokens,
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).NotTo(HaveOccurred())
			Expect(serviceAccountTokens.gets).To(Equal(1))
		})

		It("denies deleting the singleton when the cluster service account token issuer cannot be verified", func() {
			serviceAccountTokens := &fakeServiceAccountTokenClient{err: fmt.Errorf("token request forbidden")}
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Status.IssuerURL = webhookIssuerURL
			validator := &OIDCIssuerValidator{
				Client:               k8sClient,
				ServiceAccountTokens: serviceAccountTokens,
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("could not verify"))
			Expect(err.Error()).To(ContainSubstring("token request forbidden"))
			Expect(serviceAccountTokens.gets).To(Equal(1))
		})

		It("denies deleting the singleton when no cluster service account issuer guard is configured", func() {
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Status.IssuerURL = webhookIssuerURL
			validator := &OIDCIssuerValidator{
				Client: k8sClient,
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("cannot verify"))
			Expect(err.Error()).To(ContainSubstring(webhookIssuerURL))
		})

		It("denies deleting the singleton while OpenShift still uses the issuer URL", func() {
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			issuer.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: true}
			issuer.Status.IssuerURL = webhookIssuerURL
			validator := &OIDCIssuerValidator{
				Client:                        k8sClient,
				OpenShiftServiceAccountIssuer: &fakeOpenShiftServiceAccountIssuerGetter{currentIssuer: webhookIssuerURL},
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("serviceAccountIssuer still references"))
			Expect(err.Error()).To(ContainSubstring(webhookIssuerURL))
		})

		It("allows deleting the singleton after OpenShift service account issuer handoff", func() {
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			issuer.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: true}
			issuer.Status.IssuerURL = webhookIssuerURL
			validator := &OIDCIssuerValidator{
				Client:                        k8sClient,
				OpenShiftServiceAccountIssuer: &fakeOpenShiftServiceAccountIssuerGetter{currentIssuer: "https://issuer.example"},
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).NotTo(HaveOccurred())
		})

		It("keeps denying deletion after issuer management is disabled until OpenShift handoff completes", func() {
			previousIssuer := "https://previous.example"
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			issuer.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: false}
			issuer.Status.IssuerURL = webhookIssuerURL
			issuer.Status.PreviousServiceAccountIssuer = &previousIssuer
			validator := &OIDCIssuerValidator{
				Client:                        k8sClient,
				OpenShiftServiceAccountIssuer: &fakeOpenShiftServiceAccountIssuerGetter{currentIssuer: webhookIssuerURL},
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("serviceAccountIssuer still references"))
		})

		It("keeps denying deletion after OpenShift spec is removed until handoff completes", func() {
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
			issuer.Status.IssuerURL = webhookIssuerURL
			validator := &OIDCIssuerValidator{
				Client:                        k8sClient,
				OpenShiftServiceAccountIssuer: &fakeOpenShiftServiceAccountIssuerGetter{currentIssuer: webhookIssuerURL},
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("serviceAccountIssuer still references"))
		})

		It("denies deletion policy Retain while OpenShift still uses the issuer URL", func() {
			openShiftServiceAccountIssuer := &fakeOpenShiftServiceAccountIssuerGetter{currentIssuer: webhookIssuerURL}
			issuer := validWebhookOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			issuer.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyRetain
			issuer.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: true}
			issuer.Status.IssuerURL = webhookIssuerURL
			validator := &OIDCIssuerValidator{
				Client:                        k8sClient,
				OpenShiftServiceAccountIssuer: openShiftServiceAccountIssuer,
			}

			_, err := validator.ValidateDelete(ctx, issuer)

			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("serviceAccountIssuer still references"))
			Expect(openShiftServiceAccountIssuer.gets).To(Equal(1))
		})
	})
})

type fakeOpenShiftServiceAccountIssuerGetter struct {
	currentIssuer string
	gets          int
}

type fakeServiceAccountTokenClient struct {
	currentIssuer string
	err           error
	gets          int
}

func (f *fakeOpenShiftServiceAccountIssuerGetter) Get(context.Context) (string, error) {
	f.gets++
	return f.currentIssuer, nil
}

func (f *fakeServiceAccountTokenClient) CurrentIssuer(context.Context) (string, error) {
	f.gets++
	return f.currentIssuer, f.err
}

func validWebhookOIDCIssuer(name string) *workloadidentityv1alpha1.OIDCIssuer {
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

func validWebhookWorkloadIdentity(name, namespace string) *workloadidentityv1alpha1.WorkloadIdentity {
	return &workloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadidentityv1alpha1.WorkloadIdentitySpec{
			Azure: workloadidentityv1alpha1.AzureWorkloadIdentityConfig{
				SubscriptionID:                  "00000000-0000-0000-0000-000000000000",
				Location:                        "swedencentral",
				ResourceGroupName:               "rg-wi-test",
				UserAssignedIdentityName:        "uami-wi-test",
				FederatedIdentityCredentialName: "fic-wi-test",
			},
			ServiceAccount: workloadidentityv1alpha1.ServiceAccountReference{Name: "test-service-account"},
		},
	}
}

func deleteWebhookOIDCIssuer(name string) {
	issuer := &workloadidentityv1alpha1.OIDCIssuer{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Delete(ctx, issuer)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unexpected OIDCIssuer delete error: %v", err)
		return
	}

	Eventually(func(g Gomega) {
		current := &workloadidentityv1alpha1.OIDCIssuer{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, current)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}).Should(Succeed())
}

func deleteWebhookWorkloadIdentity(name, namespace string) {
	identity := &workloadidentityv1alpha1.WorkloadIdentity{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	err := k8sClient.Delete(ctx, identity)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unexpected WorkloadIdentity delete error: %v", err)
		return
	}

	Eventually(func(g Gomega) {
		current := &workloadidentityv1alpha1.WorkloadIdentity{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, current)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}).Should(Succeed())
}
