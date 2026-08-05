package signingkey

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

const (
	testSigningKeyNamespace = "kube-system"
	testSigningKeyDataKey   = "tls.key"
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

func TestPublicKeysPEMIncludesActiveAndRetiringKeys(t *testing.T) {
	activeKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	retiringKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	activeRef := azworkloadidentityv1alpha1.SecretKeyReference{Name: "active-key", Namespace: testSigningKeyNamespace, Key: testSigningKeyDataKey}
	retiringRef := azworkloadidentityv1alpha1.SecretKeyReference{Name: "retiring-key", Namespace: testSigningKeyNamespace, Key: testSigningKeyDataKey}
	reader := &getOnlyRecordingReader{delegate: fakeClient(t,
		signingKeySecretForRef(activeRef, publicKeyPEM(t, &activeKey.PublicKey)),
		signingKeySecretForRef(retiringRef, publicKeyPEM(t, &retiringKey.PublicKey)),
	)}

	keys, err := PublicKeysPEM(context.Background(), reader, azworkloadidentityv1alpha1.SigningKeySource{
		SecretRef:         activeRef,
		RetiringSecretRef: &retiringRef,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(keys) != 2 {
		t.Fatalf("keys = %d", len(keys))
	}
	if reader.listCalls != 0 {
		t.Fatalf("secret list calls = %d, want 0", reader.listCalls)
	}
	expectedGets := []client.ObjectKey{
		{Name: activeRef.Name, Namespace: activeRef.Namespace},
		{Name: retiringRef.Name, Namespace: retiringRef.Namespace},
	}
	if !slices.Equal(reader.gets, expectedGets) {
		t.Fatalf("secret get calls = %v", reader.gets)
	}
	if keys[0].State != azworkloadidentityv1alpha1.SigningKeyStateActive {
		t.Fatalf("active state = %q", keys[0].State)
	}
	if keys[1].State != azworkloadidentityv1alpha1.SigningKeyStateRetiring {
		t.Fatalf("retiring state = %q", keys[1].State)
	}
	assertPublicKeyPEM(t, keys[0].PEM)
	assertPublicKeyPEM(t, keys[1].PEM)
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
	return signingKeySecretForRef(signingKeyRef(), keyPEM)
}

func signingKeySecretForRef(ref azworkloadidentityv1alpha1.SecretKeyReference, keyPEM []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace},
		Data:       map[string][]byte{ref.Key: keyPEM},
	}
}

func signingKeyRef() azworkloadidentityv1alpha1.SecretKeyReference {
	return azworkloadidentityv1alpha1.SecretKeyReference{Name: "service-account-signing-key", Namespace: testSigningKeyNamespace, Key: testSigningKeyDataKey}
}

func fakeClient(t *testing.T, objects ...runtime.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
}

type getOnlyRecordingReader struct {
	delegate  client.Reader
	gets      []client.ObjectKey
	listCalls int
}

func (r *getOnlyRecordingReader) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	r.gets = append(r.gets, key)
	return r.delegate.Get(ctx, key, obj, opts...)
}

func (r *getOnlyRecordingReader) List(
	context.Context,
	client.ObjectList,
	...client.ListOption,
) error {
	r.listCalls++
	return errors.New("secret list is not allowed")
}
