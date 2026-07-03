package oidc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"
)

func TestDiscoveryDocument(t *testing.T) {
	docJSON, err := DiscoveryDocument("https://example.blob.core.windows.net/oidc/", algRS256, algES256)
	if err != nil {
		t.Fatal(err)
	}

	var doc discoveryDocument
	if err := json.Unmarshal(docJSON, &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Issuer != "https://example.blob.core.windows.net/oidc" {
		t.Fatalf("issuer = %q", doc.Issuer)
	}
	if doc.JWKSURI != "https://example.blob.core.windows.net/oidc/openid/v1/jwks" {
		t.Fatalf("jwks_uri = %q", doc.JWKSURI)
	}
	if len(doc.IDTokenSigningAlgValuesSupported) != 2 || doc.IDTokenSigningAlgValuesSupported[0] != algRS256 || doc.IDTokenSigningAlgValuesSupported[1] != algES256 {
		t.Fatalf("algorithms = %v", doc.IDTokenSigningAlgValuesSupported)
	}
}

func TestJWKSFromPEMRSA(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	jwksJSON, err := JWKSFromPEM(publicKeyPEM(t, &privateKey.PublicKey))
	if err != nil {
		t.Fatal(err)
	}

	var doc jwksDocument
	if err := json.Unmarshal(jwksJSON, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("keys = %d", len(doc.Keys))
	}
	if doc.Keys[0].Kty != "RSA" || doc.Keys[0].Alg != algRS256 || doc.Keys[0].Kid == "" || doc.Keys[0].N == "" || doc.Keys[0].E == "" {
		t.Fatalf("unexpected RSA JWK: %+v", doc.Keys[0])
	}
}

func TestSigningAlgorithmFromPEM(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	alg, err := SigningAlgorithmFromPEM(publicKeyPEM(t, &rsaKey.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if alg != algRS256 {
		t.Fatalf("RSA alg = %q", alg)
	}

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	alg, err = SigningAlgorithmFromPEM(publicKeyPEM(t, &ecdsaKey.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if alg != algES256 {
		t.Fatalf("ECDSA alg = %q", alg)
	}
}

func TestJWKSFromPEMECDSA(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	jwksJSON, err := JWKSFromPEM(publicKeyPEM(t, &privateKey.PublicKey))
	if err != nil {
		t.Fatal(err)
	}

	var doc jwksDocument
	if err := json.Unmarshal(jwksJSON, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("keys = %d", len(doc.Keys))
	}
	if doc.Keys[0].Kty != "EC" || doc.Keys[0].Alg != algES256 || doc.Keys[0].Crv != "P-256" || doc.Keys[0].X == "" || doc.Keys[0].Y == "" {
		t.Fatalf("unexpected EC JWK: %+v", doc.Keys[0])
	}
}

func TestJWKSFromPEMsPublishesMultipleKeys(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	jwksJSON, err := JWKSFromPEMs(publicKeyPEM(t, &rsaKey.PublicKey), publicKeyPEM(t, &ecdsaKey.PublicKey))
	if err != nil {
		t.Fatal(err)
	}

	var doc jwksDocument
	if err := json.Unmarshal(jwksJSON, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 2 {
		t.Fatalf("keys = %d", len(doc.Keys))
	}
	if doc.Keys[0].Alg != algRS256 || doc.Keys[1].Alg != algES256 {
		t.Fatalf("algorithms = %q, %q", doc.Keys[0].Alg, doc.Keys[1].Alg)
	}
}

func TestJWKSFromPEMsDeduplicatesKeys(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyPEM := publicKeyPEM(t, &privateKey.PublicKey)

	jwksJSON, err := JWKSFromPEMs(publicKeyPEM, publicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	var doc jwksDocument
	if err := json.Unmarshal(jwksJSON, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("keys = %d", len(doc.Keys))
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
