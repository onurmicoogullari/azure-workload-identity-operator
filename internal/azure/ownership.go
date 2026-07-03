package azure

import (
	"errors"
	"fmt"
	"maps"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
)

const (
	createdByOperatorTag    = "created-by-operator"
	managedByTag            = "managed-by"
	operatorAPIGroupTag     = "operator-api-group"
	operatorAPIGroupValue   = "workloadidentity.azure.micosolutions.se"
	operatorCreatedTagValue = "true"
	operatorName            = "az-workload-identity-operator"
)

func operatorOwnershipTags(uidTagKey, uid string, createdByOperator bool) map[string]*string {
	return map[string]*string{
		managedByTag:         to.Ptr(operatorName),
		uidTagKey:            to.Ptr(uid),
		createdByOperatorTag: to.Ptr(fmt.Sprintf("%t", createdByOperator)),
		operatorAPIGroupTag:  to.Ptr(operatorAPIGroupValue),
	}
}

func mergeTags(existing, desired map[string]*string) map[string]*string {
	merged := make(map[string]*string, len(existing)+len(desired))
	maps.Copy(merged, existing)
	maps.Copy(merged, desired)
	return merged
}

func hasTags(existing, desired map[string]*string) bool {
	for key, value := range desired {
		if existing[key] == nil || value == nil || *existing[key] != *value {
			return false
		}
	}
	return true
}

func wasOperatorCreatedResource(existing, createdTags map[string]*string) bool {
	if existing == nil || existing[createdByOperatorTag] == nil || *existing[createdByOperatorTag] != operatorCreatedTagValue {
		return false
	}
	return hasTags(existing, createdTags)
}

func isOperatorResourceOwnedByDifferentWorkloadIdentity(existing map[string]*string, uid string) bool {
	if existing == nil {
		return false
	}
	if existing[managedByTag] == nil || *existing[managedByTag] != operatorName {
		return false
	}
	if existing[operatorAPIGroupTag] == nil || *existing[operatorAPIGroupTag] != operatorAPIGroupValue {
		return false
	}
	return existing[workloadIdentityUIDTag] != nil && *existing[workloadIdentityUIDTag] != uid
}

func isNotFound(err error) bool {
	var responseErr *azcore.ResponseError
	return errors.As(err, &responseErr) && responseErr.StatusCode == 404
}
