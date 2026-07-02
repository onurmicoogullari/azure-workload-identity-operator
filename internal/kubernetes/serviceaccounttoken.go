package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultTokenExpirationSeconds int64 = 600

// ServiceAccountTokenClient requests service account tokens from the cluster.
type ServiceAccountTokenClient struct {
	Client             client.Client
	Namespace          string
	ServiceAccountName string
	ExpirationSeconds  int64
}

// CurrentIssuer returns the issuer from a freshly requested service account token.
func (c *ServiceAccountTokenClient) CurrentIssuer(ctx context.Context) (string, error) {
	if c == nil {
		return "", fmt.Errorf("service account token client is required")
	}
	if c.Client == nil {
		return "", fmt.Errorf("kubernetes client is required")
	}
	if c.Namespace == "" {
		return "", fmt.Errorf("service account namespace is required")
	}
	if c.ServiceAccountName == "" {
		return "", fmt.Errorf("service account name is required")
	}

	expirationSeconds := c.expirationSeconds()
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.ServiceAccountName,
			Namespace: c.Namespace,
		},
	}
	tokenRequest := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &expirationSeconds,
		},
	}

	if err := c.Client.SubResource("token").Create(ctx, serviceAccount, tokenRequest); err != nil {
		return "", fmt.Errorf("create token for ServiceAccount %s/%s: %w", c.Namespace, c.ServiceAccountName, err)
	}

	issuer, err := issuerFromJWT(tokenRequest.Status.Token)
	if err != nil {
		return "", fmt.Errorf("read issuer from ServiceAccount token: %w", err)
	}
	return issuer, nil
}

func (c *ServiceAccountTokenClient) expirationSeconds() int64 {
	if c.ExpirationSeconds > 0 {
		return c.ExpirationSeconds
	}
	return defaultTokenExpirationSeconds
}

func issuerFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("token is not a JWT")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode JWT payload: %w", err)
	}

	var claims struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("decode JWT claims: %w", err)
	}
	if claims.Issuer == "" {
		return "", fmt.Errorf("JWT issuer claim is empty")
	}
	return claims.Issuer, nil
}
