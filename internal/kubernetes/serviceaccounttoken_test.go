package kubernetes

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
)

func TestCurrentIssuerRejectsNilClient(t *testing.T) {
	var client *ServiceAccountTokenClient

	if _, err := client.CurrentIssuer(context.Background()); err == nil {
		t.Fatal("CurrentIssuer returned nil error for nil client")
	}
}

func TestIssuerFromJWT(t *testing.T) {
	token := jwtWithPayload(`{"iss":"https://issuer.example","sub":"system:serviceaccount:test:manager"}`)

	issuer, err := issuerFromJWT(token)
	if err != nil {
		t.Fatalf("issuerFromJWT returned error: %v", err)
	}
	if issuer != "https://issuer.example" {
		t.Fatalf("issuerFromJWT issuer = %q, want %q", issuer, "https://issuer.example")
	}
}

func TestIssuerFromJWTRejectsMalformedToken(t *testing.T) {
	if _, err := issuerFromJWT("not-a-jwt"); err == nil {
		t.Fatal("issuerFromJWT returned nil error for malformed token")
	}
}

func TestIssuerFromJWTRejectsEmptyIssuer(t *testing.T) {
	if _, err := issuerFromJWT(jwtWithPayload(`{"sub":"system:serviceaccount:test:manager"}`)); err == nil {
		t.Fatal("issuerFromJWT returned nil error for token without issuer")
	}
}

func jwtWithPayload(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	return fmt.Sprintf("%s.%s.%s", header, encodedPayload, signature)
}
