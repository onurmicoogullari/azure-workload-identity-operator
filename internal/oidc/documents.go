package oidc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
)

const jwksPath = "openid/v1/jwks"

const (
	algRS256 = "RS256"
	algES256 = "ES256"
)

type discoveryDocument struct {
	Issuer                           string   `json:"issuer"`
	JWKSURI                          string   `json:"jwks_uri"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

type PublicKeyMetadata struct {
	KeyID     string
	Algorithm string
}

func DiscoveryDocument(issuerURL string, signingAlgorithms ...string) ([]byte, error) {
	issuerURL = strings.TrimRight(issuerURL, "/")
	if issuerURL == "" {
		return nil, fmt.Errorf("issuer URL is required")
	}
	if len(signingAlgorithms) == 0 {
		return nil, fmt.Errorf("at least one signing algorithm is required")
	}

	doc := discoveryDocument{
		Issuer:                           issuerURL,
		JWKSURI:                          issuerURL + "/" + jwksPath,
		ResponseTypesSupported:           []string{"id_token"},
		SubjectTypesSupported:            []string{"public"},
		IDTokenSigningAlgValuesSupported: signingAlgorithms,
	}

	return json.Marshal(doc)
}

func SigningAlgorithmFromPEM(publicKeyPEM []byte) (string, error) {
	metadata, err := PublicKeyMetadataFromPEM(publicKeyPEM)
	if err != nil {
		return "", err
	}
	return metadata.Algorithm, nil
}

func PublicKeyMetadataFromPEM(publicKeyPEM []byte) (PublicKeyMetadata, error) {
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return PublicKeyMetadata{}, fmt.Errorf("public key PEM is invalid")
	}

	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return PublicKeyMetadata{}, fmt.Errorf("parse public key: %w", err)
	}

	switch key := publicKey.(type) {
	case *rsa.PublicKey:
		return PublicKeyMetadata{KeyID: keyID(mustMarshalPKIXPublicKey(key)), Algorithm: algRS256}, nil
	case *ecdsa.PublicKey:
		if key.Curve != elliptic.P256() {
			return PublicKeyMetadata{}, fmt.Errorf("unsupported ECDSA curve")
		}
		return PublicKeyMetadata{KeyID: keyID(mustMarshalPKIXPublicKey(key)), Algorithm: algES256}, nil
	default:
		return PublicKeyMetadata{}, fmt.Errorf("unsupported public key type %T", publicKey)
	}
}

func JWKSFromPEM(publicKeyPEM []byte) ([]byte, error) {
	return JWKSFromPEMs(publicKeyPEM)
}

func JWKSFromPEMs(publicKeyPEMs ...[]byte) ([]byte, error) {
	if len(publicKeyPEMs) == 0 {
		return nil, fmt.Errorf("at least one public key PEM is required")
	}

	keys := make([]jwk, 0, len(publicKeyPEMs))
	seen := map[string]struct{}{}

	for _, publicKeyPEM := range publicKeyPEMs {
		key, err := jwkFromPEM(publicKeyPEM)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[key.Kid]; ok {
			continue
		}
		seen[key.Kid] = struct{}{}
		keys = append(keys, key)
	}

	return json.Marshal(jwksDocument{Keys: keys})
}

func jwkFromPEM(publicKeyPEM []byte) (jwk, error) {
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return jwk{}, fmt.Errorf("public key PEM is invalid")
	}

	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return jwk{}, fmt.Errorf("parse public key: %w", err)
	}

	key, err := publicKeyToJWK(publicKey)
	if err != nil {
		return jwk{}, err
	}

	return key, nil
}

func publicKeyToJWK(publicKey any) (jwk, error) {
	switch key := publicKey.(type) {
	case *rsa.PublicKey:
		return jwk{
			Kty: "RSA",
			Use: "sig",
			Kid: keyID(mustMarshalPKIXPublicKey(key)),
			Alg: algRS256,
			N:   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}, nil
	case *ecdsa.PublicKey:
		if key.Curve != elliptic.P256() {
			return jwk{}, fmt.Errorf("unsupported ECDSA curve")
		}
		publicKeyBytes, err := key.Bytes()
		if err != nil {
			return jwk{}, fmt.Errorf("encode ECDSA public key: %w", err)
		}
		if len(publicKeyBytes) != 65 || publicKeyBytes[0] != 4 {
			return jwk{}, fmt.Errorf("unsupported ECDSA public key encoding")
		}
		return jwk{
			Kty: "EC",
			Use: "sig",
			Kid: keyID(mustMarshalPKIXPublicKey(key)),
			Alg: algES256,
			Crv: "P-256",
			X:   base64.RawURLEncoding.EncodeToString(publicKeyBytes[1:33]),
			Y:   base64.RawURLEncoding.EncodeToString(publicKeyBytes[33:]),
		}, nil
	default:
		return jwk{}, fmt.Errorf("unsupported public key type %T", publicKey)
	}
}

func keyID(publicKeyDER []byte) string {
	sum := sha256.Sum256(publicKeyDER)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func mustMarshalPKIXPublicKey(publicKey any) []byte {
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		panic(err)
	}
	return der
}
