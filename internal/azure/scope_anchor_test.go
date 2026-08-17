package azure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateScopeAnchor(t *testing.T) {
	configured, err := NewScope(testSubscriptionID, testResourceGroupName, testAzureLocation)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		values     map[string]string
		missingKey string
		wantError  string
	}{
		{
			name: "matching",
			values: map[string]string{
				subscriptionIDAnchorKey:    testSubscriptionID,
				resourceGroupNameAnchorKey: testResourceGroupName,
				locationAnchorKey:          testAzureLocation,
			},
		},
		{
			name: "matching normalized values",
			values: map[string]string{
				subscriptionIDAnchorKey:    "  " + strings.ToUpper(testSubscriptionID) + "\n",
				resourceGroupNameAnchorKey: strings.ToUpper(testResourceGroupName),
				locationAnchorKey:          "  SWEDENCENTRAL\n",
			},
		},
		{
			name: "subscription ID mismatch",
			values: map[string]string{
				subscriptionIDAnchorKey:    "10000000-0000-0000-0000-000000000000",
				resourceGroupNameAnchorKey: testResourceGroupName,
				locationAnchorKey:          testAzureLocation,
			},
			wantError: "subscriptionId does not match",
		},
		{
			name: "resource group mismatch",
			values: map[string]string{
				subscriptionIDAnchorKey:    testSubscriptionID,
				resourceGroupNameAnchorKey: "different-resource-group",
				locationAnchorKey:          testAzureLocation,
			},
			wantError: "resourceGroupName does not match",
		},
		{
			name: "location mismatch",
			values: map[string]string{
				subscriptionIDAnchorKey:    testSubscriptionID,
				resourceGroupNameAnchorKey: testResourceGroupName,
				locationAnchorKey:          "westus",
			},
			wantError: "location does not match",
		},
		{
			name: "missing value",
			values: map[string]string{
				subscriptionIDAnchorKey:    testSubscriptionID,
				resourceGroupNameAnchorKey: testResourceGroupName,
				locationAnchorKey:          testAzureLocation,
			},
			missingKey: resourceGroupNameAnchorKey,
			wantError:  "read Azure startup scope resourceGroupName",
		},
		{
			name: "malformed value",
			values: map[string]string{
				subscriptionIDAnchorKey:    "not-a-subscription-id",
				resourceGroupNameAnchorKey: testResourceGroupName,
				locationAnchorKey:          testAzureLocation,
			},
			wantError: "parse Azure startup scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			for key, value := range tt.values {
				if key == tt.missingKey {
					continue
				}
				if err := os.WriteFile(filepath.Join(directory, key), []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			err := ValidateScopeAnchor(configured, directory)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("ValidateScopeAnchor() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("ValidateScopeAnchor() error = %v, want error containing %q", err, tt.wantError)
			}
		})
	}
}
