package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

func main() {
	vaultURL := fmt.Sprintf("https://%s.vault.azure.net/", os.Getenv("KEY_VAULT_NAME"))
	secretName := os.Getenv("KEY_VAULT_SECRET_NAME")
	clientID := os.Getenv("AZURE_CLIENT_ID")
	readTimeoutSeconds := envInt("KEY_VAULT_READ_TIMEOUT_SECONDS", 300)

	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		fail(vaultURL, secretName, clientID, "create credential: %v", err)
	}

	client, err := azsecrets.NewClient(vaultURL, credential, nil)
	if err != nil {
		fail(vaultURL, secretName, clientID, "create key vault client: %v", err)
	}

	deadline := time.Now().Add(time.Duration(readTimeoutSeconds) * time.Second)
	attempt := 1
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		secret, err := client.GetSecret(ctx, secretName, "", nil)
		cancel()
		if err == nil && secret.Value != nil {
			value := strings.TrimSpace(*secret.Value)
			fmt.Printf("Successfully retrieved secret %s from key vault %s using identity client-id %s, value: %q\n", secretName, vaultURL, clientID, value)
			return
		}
		if err == nil {
			err = fmt.Errorf("secret has no value")
		}
		if time.Now().After(deadline) {
			fail(vaultURL, secretName, clientID, "%v", err)
		}
		fmt.Fprintf(os.Stderr, "Secret read attempt %d failed; retrying: %v\n", attempt, err)
		attempt++
		time.Sleep(10 * time.Second)
	}
}

func fail(vaultURL, secretName, clientID, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Failed to retrieve value of secret %s from key vault %s using identity client-id %s: %s\n", secretName, vaultURL, clientID, fmt.Sprintf(format, args...))
	os.Exit(1)
}

func envInt(name string, defaultValue int) int {
	value := os.Getenv(name)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return defaultValue
	}
	return parsed
}
