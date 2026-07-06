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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

var _ = Describe("Controller event predicates", func() {
	Describe("Primary resources", func() {
		predicate := primaryResourcePredicate()

		It("ignores status-only updates", func() {
			oldIdentity := validWorkloadIdentity("test-workload", testWorkloadNamespace)
			oldIdentity.Generation = 1
			newIdentity := oldIdentity.DeepCopy()
			now := metav1.Now()
			newIdentity.Status.LastReconciledTime = &now

			Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldIdentity, ObjectNew: newIdentity})).To(BeFalse())
		})

		It("accepts spec generation changes", func() {
			oldIdentity := validWorkloadIdentity("test-workload", testWorkloadNamespace)
			oldIdentity.Generation = 1
			newIdentity := oldIdentity.DeepCopy()
			newIdentity.Generation = 2

			Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldIdentity, ObjectNew: newIdentity})).To(BeTrue())
		})

		It("accepts deletion timestamp changes", func() {
			oldIdentity := validWorkloadIdentity("test-workload", testWorkloadNamespace)
			oldIdentity.Generation = 1
			newIdentity := oldIdentity.DeepCopy()
			now := metav1.Now()
			newIdentity.DeletionTimestamp = &now

			Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldIdentity, ObjectNew: newIdentity})).To(BeTrue())
		})
	})

	Describe("OIDCIssuer dependency updates", func() {
		predicate := oidcIssuerDependencyPredicate()

		It("ignores heartbeat-only status updates", func() {
			oldIssuer := readyOIDCIssuerForPredicate()
			newIssuer := oldIssuer.DeepCopy()
			now := metav1.Now()
			newIssuer.Status.LastReconciledTime = &now

			Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldIssuer, ObjectNew: newIssuer})).To(BeFalse())
		})

		It("accepts issuer URL changes", func() {
			oldIssuer := readyOIDCIssuerForPredicate()
			newIssuer := oldIssuer.DeepCopy()
			newIssuer.Status.IssuerURL = "https://new.example/oidc"

			Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldIssuer, ObjectNew: newIssuer})).To(BeTrue())
		})

		It("accepts readiness changes", func() {
			oldIssuer := readyOIDCIssuerForPredicate()
			newIssuer := oldIssuer.DeepCopy()
			newIssuer.Status.Conditions[0].Status = metav1.ConditionFalse

			Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldIssuer, ObjectNew: newIssuer})).To(BeTrue())
		})
	})

	Describe("ServiceAccount dependency updates", func() {
		predicate := serviceAccountDependencyPredicate()

		It("ignores unrelated metadata updates", func() {
			oldServiceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: testServiceAccountName, Namespace: testWorkloadNamespace}}
			newServiceAccount := oldServiceAccount.DeepCopy()
			newServiceAccount.Labels = map[string]string{"unrelated": "changed"}

			Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldServiceAccount, ObjectNew: newServiceAccount})).To(BeFalse())
		})

		It("accepts workload identity annotation changes", func() {
			oldServiceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: testServiceAccountName, Namespace: testWorkloadNamespace}}
			newServiceAccount := oldServiceAccount.DeepCopy()
			newServiceAccount.Annotations = map[string]string{serviceAccountClientID: "changed-client-id"}

			Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldServiceAccount, ObjectNew: newServiceAccount})).To(BeTrue())
		})
	})

	It("allows only create and delete events for existence dependencies", func() {
		predicate := createDeleteOnlyPredicate()
		identity := validWorkloadIdentity("test-workload", testWorkloadNamespace)

		Expect(predicate.Create(event.CreateEvent{Object: identity})).To(BeTrue())
		Expect(predicate.Update(event.UpdateEvent{ObjectOld: identity, ObjectNew: identity.DeepCopy()})).To(BeFalse())
		Expect(predicate.Delete(event.DeleteEvent{Object: identity})).To(BeTrue())
	})
})

func readyOIDCIssuerForPredicate() *workloadidentityv1alpha1.OIDCIssuer {
	issuer := validOIDCIssuer(workloadidentityv1alpha1.OIDCIssuerName)
	issuer.Generation = 1
	issuer.Status.IssuerURL = testIssuerURL
	issuer.Status.Conditions = []metav1.Condition{{
		Type:   string(workloadidentityv1alpha1.OIDCIssuerConditionReady),
		Status: metav1.ConditionTrue,
	}}
	return issuer
}
