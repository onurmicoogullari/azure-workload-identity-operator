package signingkey

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

const publicKeyPEMType = "PUBLIC KEY"

type PublicKey struct {
	PEM   []byte
	State azworkloadidentityv1alpha1.SigningKeyState
}

func PublicKeysPEM(ctx context.Context, reader client.Reader, source azworkloadidentityv1alpha1.SigningKeySource) ([]PublicKey, error) {
	refs := []azworkloadidentityv1alpha1.SecretKeyReference{source.SecretRef}
	if source.RetiringSecretRef != nil {
		refs = append(refs, *source.RetiringSecretRef)
	}
	keys := make([]PublicKey, 0, len(refs))

	for i, ref := range refs {
		keyPEM, err := PublicKeyPEM(ctx, reader, ref)
		if err != nil {
			return nil, err
		}

		state := azworkloadidentityv1alpha1.SigningKeyStateRetiring
		if i == 0 {
			state = azworkloadidentityv1alpha1.SigningKeyStateActive
		}
		keys = append(keys, PublicKey{PEM: keyPEM, State: state})
	}

	return keys, nil
}

func PublicKeyPEM(ctx context.Context, reader client.Reader, ref azworkloadidentityv1alpha1.SecretKeyReference) ([]byte, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}
	if err := reader.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("get signing key secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	keyPEM, ok := secret.Data[ref.Key]
	if !ok {
		return nil, fmt.Errorf("signing key %q not found in secret %s/%s", ref.Key, ref.Namespace, ref.Name)
	}

	publicKey, err := publicKey(keyPEM)
	if err != nil {
		return nil, err
	}

	publicKeyDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMType, Bytes: publicKeyDER}), nil
}

func publicKey(keyPEM []byte) (any, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("signing key PEM is invalid")
	}

	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		return key, nil
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return &key.PublicKey, nil
	}

	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return &key.PublicKey, nil
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse signing key: %w", err)
	}

	switch key := key.(type) {
	case *rsa.PrivateKey:
		return &key.PublicKey, nil
	case *ecdsa.PrivateKey:
		return &key.PublicKey, nil
	case *rsa.PublicKey, *ecdsa.PublicKey:
		return key, nil
	default:
		return nil, fmt.Errorf("unsupported signing key type %T", key)
	}
}
