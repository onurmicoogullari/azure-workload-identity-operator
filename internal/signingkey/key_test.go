package signingkey

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

func TestPublicKeyPEMFromPublicKey(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	got := publicKeyPEMFromSecret(t, publicKeyPEM(t, &privateKey.PublicKey))
	assertPublicKeyPEM(t, got)
}

func TestPublicKeyPEMFromRSAPrivateKey(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	got := publicKeyPEMFromSecret(t, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}))
	assertPublicKeyPEM(t, got)
}

func TestPublicKeyPEMFromECPrivateKey(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}

	got := publicKeyPEMFromSecret(t, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	assertPublicKeyPEM(t, got)
}

func TestPublicKeyPEMMissingKey(t *testing.T) {
	secret := signingKeySecret(nil)
	secret.Data = map[string][]byte{}
	k8sClient := fakeClient(t, secret)

	_, err := PublicKeyPEM(context.Background(), k8sClient, signingKeyRef())
	if err == nil {
		t.Fatal("expected error")
	}
}

func publicKeyPEMFromSecret(t *testing.T, keyPEM []byte) []byte {
	t.Helper()

	got, err := PublicKeyPEM(context.Background(), fakeClient(t, signingKeySecret(keyPEM)), signingKeyRef())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertPublicKeyPEM(t *testing.T, keyPEM []byte) {
	t.Helper()

	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != publicKeyPEMType {
		t.Fatalf("expected PUBLIC KEY PEM, got %q", keyPEM)
	}
	if _, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil {
		t.Fatal(err)
	}
}

func publicKeyPEM(t *testing.T, publicKey any) []byte {
	t.Helper()

	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMType, Bytes: der})
}

func signingKeySecret(keyPEM []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "service-account-signing-key", Namespace: "kube-system"},
		Data:       map[string][]byte{"tls.key": keyPEM},
	}
}

func signingKeyRef() azworkloadidentityv1alpha1.SecretKeyReference {
	return azworkloadidentityv1alpha1.SecretKeyReference{Name: "service-account-signing-key", Namespace: "kube-system", Key: "tls.key"}
}

func fakeClient(t *testing.T, objects ...runtime.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
}
