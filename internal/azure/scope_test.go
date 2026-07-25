package azure

import "testing"

const (
	testResourceGroupName = "rg-test"
	testAzureLocation     = "swedencentral"
)

func TestNewScopeValidatesRequiredStartupConfiguration(t *testing.T) {
	tests := []struct {
		name              string
		subscriptionID    string
		resourceGroupName string
		location          string
	}{
		{
			name:              "missing subscription",
			resourceGroupName: testResourceGroupName,
			location:          testAzureLocation,
		},
		{
			name:              "invalid subscription",
			subscriptionID:    "not-a-uuid",
			resourceGroupName: testResourceGroupName,
			location:          testAzureLocation,
		},
		{
			name:              "invalid resource group",
			subscriptionID:    testSubscriptionID,
			resourceGroupName: "rg-test.",
			location:          testAzureLocation,
		},
		{
			name:              "missing location",
			subscriptionID:    testSubscriptionID,
			resourceGroupName: testResourceGroupName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewScope(tt.subscriptionID, tt.resourceGroupName, tt.location); err == nil {
				t.Fatal("expected invalid scope")
			}
		})
	}
}

func TestScopeRetainsValidatedValues(t *testing.T) {
	scope, err := NewScope(testSubscriptionID, testResourceGroupName, testAzureLocation)
	if err != nil {
		t.Fatal(err)
	}
	if scope.subscriptionID != testSubscriptionID ||
		scope.resourceGroupName != testResourceGroupName ||
		scope.location != testAzureLocation {
		t.Fatalf(
			"scope = %q/%q/%q",
			scope.subscriptionID,
			scope.resourceGroupName,
			scope.location,
		)
	}
}
