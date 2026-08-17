package azure

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	subscriptionIDAnchorKey    = "subscriptionId"
	resourceGroupNameAnchorKey = "resourceGroupName"
	locationAnchorKey          = "location"
)

// ValidateScopeAnchor verifies that the configured Azure scope matches the
// durable values projected from the installation's retained ConfigMap.
func ValidateScopeAnchor(configured Scope, directory string) error {
	anchored, err := readScopeAnchor(directory)
	if err != nil {
		return err
	}
	if field := scopeMismatchField(configured, anchored); field != "" {
		return fmt.Errorf("configured Azure scope %s does not match the retained startup scope", field)
	}
	return nil
}

func scopeMismatchField(configured, anchored Scope) string {
	switch {
	case configured.subscriptionID != anchored.subscriptionID:
		return subscriptionIDAnchorKey
	case !strings.EqualFold(configured.resourceGroupName, anchored.resourceGroupName):
		return resourceGroupNameAnchorKey
	case configured.location != anchored.location:
		return locationAnchorKey
	default:
		return ""
	}
}

func readScopeAnchor(directory string) (Scope, error) {
	if directory == "" {
		return Scope{}, fmt.Errorf("azure startup scope directory is required")
	}

	values := make(map[string]string, 3)
	for _, key := range []string{subscriptionIDAnchorKey, resourceGroupNameAnchorKey, locationAnchorKey} {
		value, err := os.ReadFile(filepath.Join(directory, key))
		if err != nil {
			return Scope{}, fmt.Errorf("read Azure startup scope %s: %w", key, err)
		}
		values[key] = string(value)
	}

	scope, err := NewScope(
		values[subscriptionIDAnchorKey],
		values[resourceGroupNameAnchorKey],
		values[locationAnchorKey],
	)
	if err != nil {
		return Scope{}, fmt.Errorf("parse Azure startup scope: %w", err)
	}
	return scope, nil
}
