package azure

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/signingkey"
)

func TestIssuerURL(t *testing.T) {
	issuer := testOIDCIssuer()

	if got := issuerURL(issuer); got != "https://oidctest123.blob.core.windows.net/oidc" {
		t.Fatalf("issuerURL = %q", got)
	}
}

func TestResourceTags(t *testing.T) {
	issuer := testOIDCIssuer()
	tags := resourceTags(issuer, true)

	if *tags[managedByTag] != operatorName {
		t.Fatalf("managed-by tag = %q", *tags[managedByTag])
	}
	if *tags["oidc-issuer-uid"] != "test-uid" {
		t.Fatalf("oidc-issuer-uid tag = %q", *tags["oidc-issuer-uid"])
	}
	if *tags[createdByOperatorTag] != operatorCreatedTagValue {
		t.Fatalf("created-by-operator tag = %q", *tags[createdByOperatorTag])
	}
	if *tags[operatorAPIGroupTag] != operatorAPIGroupValue {
		t.Fatalf("operator-api-group tag = %q", *tags[operatorAPIGroupTag])
	}
}

func TestMergeTagsPreservesExistingTags(t *testing.T) {
	merged := mergeTags(
		map[string]*string{"environment": to.Ptr("dev"), managedByTag: to.Ptr("someone-else")},
		map[string]*string{managedByTag: to.Ptr(operatorName)},
	)

	if *merged["environment"] != "dev" {
		t.Fatalf("environment tag = %q", *merged["environment"])
	}
	if *merged[managedByTag] != operatorName {
		t.Fatalf("managed-by tag = %q", *merged[managedByTag])
	}
}

func TestWasCreatedByOperator(t *testing.T) {
	issuer := testOIDCIssuer()

	if !wasOIDCIssuerResourceCreatedByOperator(resourceTags(issuer, true), issuer) {
		t.Fatal("expected created tags to match")
	}
	if wasOIDCIssuerResourceCreatedByOperator(resourceTags(issuer, false), issuer) {
		t.Fatal("expected adopted tags not to match")
	}
	if wasOIDCIssuerResourceCreatedByOperator(map[string]*string{managedByTag: to.Ptr("someone-else")}, issuer) {
		t.Fatal("expected unrelated tags not to match")
	}
}

func TestBuildOIDCDocumentsPublishesActiveAndRetiringKeys(t *testing.T) {
	activeKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	retiringKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	activeRef := azworkloadidentityv1alpha1.SecretKeyReference{Name: "active-key", Namespace: "kube-system", Key: "tls.key"}
	retiringRef := azworkloadidentityv1alpha1.SecretKeyReference{Name: "retiring-key", Namespace: "kube-system", Key: "tls.key"}
	issuer := testOIDCIssuer()
	issuer.Spec.SigningKey = azworkloadidentityv1alpha1.SigningKeySource{
		SecretRef:         activeRef,
		RetiringSecretRef: &retiringRef,
	}

	documents, err := buildOIDCDocuments(
		context.Background(),
		fakeKubernetesClient(t,
			signingKeySecret(activeRef, publicKeyPEM(t, &activeKey.PublicKey)),
			signingKeySecret(retiringRef, publicKeyPEM(t, &retiringKey.PublicKey)),
		),
		issuer,
		"https://issuer.example",
	)
	if err != nil {
		t.Fatal(err)
	}

	var discovery struct {
		Algorithms []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := json.Unmarshal(documents.Discovery, &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.Algorithms) != 2 || discovery.Algorithms[0] != "RS256" || discovery.Algorithms[1] != "ES256" {
		t.Fatalf("algorithms = %v", discovery.Algorithms)
	}

	var jwks struct {
		Keys []struct {
			KID string `json:"kid"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(documents.JWKS, &jwks); err != nil {
		t.Fatal(err)
	}
	if len(jwks.Keys) != 2 {
		t.Fatalf("jwks keys = %d", len(jwks.Keys))
	}
	if len(documents.SigningKeys) != 2 {
		t.Fatalf("status signing keys = %d", len(documents.SigningKeys))
	}
	if documents.SigningKeys[0].State != azworkloadidentityv1alpha1.SigningKeyStateActive {
		t.Fatalf("active key state = %q", documents.SigningKeys[0].State)
	}
	if documents.SigningKeys[1].State != azworkloadidentityv1alpha1.SigningKeyStateRetiring {
		t.Fatalf("retiring key state = %q", documents.SigningKeys[1].State)
	}
	if documents.SigningKeys[0].KID != jwks.Keys[0].KID || documents.SigningKeys[1].KID != jwks.Keys[1].KID {
		t.Fatalf("status kids = %q, %q; jwks kids = %q, %q", documents.SigningKeys[0].KID, documents.SigningKeys[1].KID, jwks.Keys[0].KID, jwks.Keys[1].KID)
	}
}

func TestPublishedSigningKeysDeduplicatesRetiringKey(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyPEM := publicKeyPEM(t, &privateKey.PublicKey)

	signingKeys, algorithms, publicKeyPEMs, err := publishedSigningKeys([]signingkey.PublicKey{
		{PEM: publicKeyPEM, State: azworkloadidentityv1alpha1.SigningKeyStateActive},
		{PEM: publicKeyPEM, State: azworkloadidentityv1alpha1.SigningKeyStateRetiring},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(signingKeys) != 1 || len(algorithms) != 1 || len(publicKeyPEMs) != 1 {
		t.Fatalf("signingKeys=%d algorithms=%d publicKeyPEMs=%d", len(signingKeys), len(algorithms), len(publicKeyPEMs))
	}
	if signingKeys[0].State != azworkloadidentityv1alpha1.SigningKeyStateActive {
		t.Fatalf("state = %q", signingKeys[0].State)
	}
}

func testOIDCIssuer() *azworkloadidentityv1alpha1.OIDCIssuer {
	return &azworkloadidentityv1alpha1.OIDCIssuer{
		ObjectMeta: metav1.ObjectMeta{Name: azworkloadidentityv1alpha1.OIDCIssuerName, UID: types.UID("test-uid")},
		Spec: azworkloadidentityv1alpha1.OIDCIssuerSpec{
			Azure: azworkloadidentityv1alpha1.AzureOIDCIssuerConfig{
				StorageAccountName: "oidctest123",
				BlobContainerName:  "oidc",
			},
		},
	}
}

func signingKeySecret(ref azworkloadidentityv1alpha1.SecretKeyReference, keyPEM []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace},
		Data:       map[string][]byte{ref.Key: keyPEM},
	}
}

func publicKeyPEM(t *testing.T, publicKey any) []byte {
	t.Helper()

	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func fakeKubernetesClient(t *testing.T, objects ...runtime.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
}
