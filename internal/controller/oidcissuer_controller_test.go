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
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/oidc"
)

const testIssuerURL = "https://oidctest123.blob.core.windows.net/oidc"

type fakeOIDCDocumentPublisher struct {
	published  oidc.PublishedDocuments
	publishErr error
	deleteErr  error
	publishes  int
	deletes    int
}

type fakeServiceAccountIssuerUpdater struct {
	issuerURL string
	updates   int
	err       error
}

func (f *fakeServiceAccountIssuerUpdater) UpdateServiceAccountIssuer(_ context.Context, issuerURL string) error {
	f.updates++
	f.issuerURL = issuerURL
	return f.err
}

func (f *fakeOIDCDocumentPublisher) Publish(context.Context, *workloadidentityv1alpha1.OIDCIssuer) (oidc.PublishedDocuments, error) {
	f.publishes++
	return f.published, f.publishErr
}

func (f *fakeOIDCDocumentPublisher) Delete(context.Context, *workloadidentityv1alpha1.OIDCIssuer) error {
	f.deletes++
	return f.deleteErr
}

var _ = Describe("OIDCIssuer Controller", func() {
	Context("When reconciling a resource", func() {
		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}

		BeforeEach(func() {
			deleteOIDCIssuer(ctx, typeNamespacedName)
		})

		AfterEach(func() {
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

			controllerReconciler := &OIDCIssuerReconciler{
				Client:    k8sClient,
				Scheme:    k8sClient.Scheme(),
				Publisher: publisher,
			}

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

		It("updates OpenShift service account issuer when enabled", func() {
			publisher := &fakeOIDCDocumentPublisher{published: oidc.PublishedDocuments{IssuerURL: testIssuerURL}}
			updater := &fakeServiceAccountIssuerUpdater{}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			resource.Spec.OpenShift = &workloadidentityv1alpha1.OpenShiftOIDCIssuerConfig{UpdateServiceAccountIssuer: true}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := &OIDCIssuerReconciler{
				Client:                      k8sClient,
				Scheme:                      k8sClient.Scheme(),
				Publisher:                   publisher,
				ServiceAccountIssuerUpdater: updater,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(updater.updates).To(Equal(1))
			Expect(updater.issuerURL).To(Equal(testIssuerURL))
		})

		It("does not update OpenShift service account issuer when disabled", func() {
			publisher := &fakeOIDCDocumentPublisher{published: oidc.PublishedDocuments{IssuerURL: testIssuerURL}}
			updater := &fakeServiceAccountIssuerUpdater{}
			resource := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := &OIDCIssuerReconciler{
				Client:                      k8sClient,
				Scheme:                      k8sClient.Scheme(),
				Publisher:                   publisher,
				ServiceAccountIssuerUpdater: updater,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(updater.updates).To(Equal(0))
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

			controllerReconciler := &OIDCIssuerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

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

			controllerReconciler := &OIDCIssuerReconciler{
				Client:    k8sClient,
				Scheme:    k8sClient.Scheme(),
				Publisher: publisher,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(publisher.deletes).To(Equal(1))
		})

	})
})

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
