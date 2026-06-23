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

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

const publicKeyPEMType = "PUBLIC KEY"

func PublicKeyPEM(ctx context.Context, c client.Client, ref azworkloadidentityv1alpha1.SecretKeyReference) ([]byte, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}
	if err := c.Get(ctx, key, secret); err != nil {
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
