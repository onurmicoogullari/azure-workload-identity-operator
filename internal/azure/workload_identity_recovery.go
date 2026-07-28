package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	recoveryReasonFederatedIdentityCredentialAmbiguous = "FederatedIdentityCredentialAmbiguous"
	recoveryReasonRecoveryMarkerConflict               = "RecoveryMarkerConflict"
	recoveryReasonUserAssignedIdentityConflict         = "UserAssignedIdentityConflict"
	recoveryReasonUserAssignedIdentityNotFound         = "UserAssignedIdentityNotFound"
)

type WorkloadIdentityRecoveryManager struct {
	Credential     azcore.TokenCredential
	Scope          Scope
	clientsFactory func() (*identityClients, error)
}

func (m *WorkloadIdentityRecoveryManager) clients() (*identityClients, error) {
	if m.clientsFactory != nil {
		return m.clientsFactory()
	}
	return newIdentityClients(m.Scope, m.Credential)
}

func (m *WorkloadIdentityRecoveryManager) loadAndValidateRecoveryState(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) (*identityClients, armmsi.Identity, identityRecoveryStage, error) {
	clients, err := m.clients()
	if err != nil {
		return nil, armmsi.Identity{}, recoveryStageNotStarted, err
	}
	uami, err := clients.getRecoveryUserAssignedIdentity(ctx, identity)
	if err != nil {
		return nil, armmsi.Identity{}, recoveryStageNotStarted, err
	}
	stage, err := recoveryStage(recovery, identity, uami)
	if err != nil {
		return nil, armmsi.Identity{}, recoveryStageNotStarted, err
	}
	if err := validateRecoveryIdentityPlan(uami, plan); err != nil {
		return nil, armmsi.Identity{}, recoveryStageNotStarted, err
	}
	return clients, uami, stage, nil
}

func (m *WorkloadIdentityRecoveryManager) Inspect(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	issuerURL, subject string,
) (*workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan, error) {
	clients, err := m.clients()
	if err != nil {
		return nil, err
	}

	uami, err := clients.getRecoveryUserAssignedIdentity(ctx, identity)
	if err != nil {
		return nil, err
	}
	if _, err := recoveryStage(recovery, identity, uami); err != nil {
		return nil, err
	}
	credentials, err := clients.federatedCredentials.List(
		ctx,
		clients.scope.resourceGroupName,
		userAssignedIdentityName(identity),
	)
	if err != nil {
		return nil, err
	}
	if err := validateRecoverableFederatedIdentityCredentialSet(clients.scope, identity, credentials); err != nil {
		return nil, err
	}

	return &workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan{
		UserAssignedIdentity: recoveryUserAssignedIdentity(uami),
		FederatedIdentityCredential: workloadidentityv1alpha1.WorkloadIdentityRecoveryFederatedIdentityCredential{
			Issuer:    issuerURL,
			Subject:   subject,
			Audiences: []string{azureADTokenExchangeAudience},
		},
	}, nil
}

func (m *WorkloadIdentityRecoveryManager) MarkInProgress(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	clients, uami, stage, err := m.loadAndValidateRecoveryState(ctx, recovery, identity, plan)
	if err != nil {
		return err
	}
	if stage != recoveryStageNotStarted {
		return nil
	}

	tags := mergeTags(uami.Tags, map[string]*string{
		workloadIdentityRecoveryUIDTag:       to.Ptr(string(recovery.UID)),
		workloadIdentityRecoveryTargetUIDTag: to.Ptr(string(identity.UID)),
	})
	if err := clients.updateRecoveryTags(ctx, identity, tags); err != nil {
		return fmt.Errorf("mark user assigned identity recovery in progress: %w", err)
	}
	return clients.verifyRecoveryStage(
		ctx,
		recovery,
		identity,
		plan,
		recoveryStageInProgress,
		"verify user assigned identity recovery marker",
	)
}

func (m *WorkloadIdentityRecoveryManager) EnsureFederatedIdentityCredential(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	clients, _, stage, err := m.loadAndValidateRecoveryState(ctx, recovery, identity, plan)
	if err != nil {
		return err
	}
	if stage == recoveryStageNotStarted {
		return recoveryMarkerNotActive()
	}

	credentialName := identity.Spec.Azure.FederatedIdentityCredentialName
	desired := desiredRecoveryFederatedIdentityCredential(plan)
	if stage == recoveryStageInProgress {
		if _, err := clients.federatedCredentials.CreateOrUpdate(
			ctx,
			clients.scope.resourceGroupName,
			userAssignedIdentityName(identity),
			credentialName,
			desired,
			nil,
		); err != nil {
			return fmt.Errorf("recover federated identity credential: %w", err)
		}
	}
	return clients.validateCommittedFederatedIdentityCredential(
		ctx,
		identity,
		plan,
		"after recovery repair",
	)
}

// Commit transfers ownership while deliberately retaining the Azure recovery
// fence. The fence is cleared only after Kubernetes persists CommitVerified.
func (m *WorkloadIdentityRecoveryManager) Commit(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	clients, uami, stage, err := m.loadAndValidateRecoveryState(ctx, recovery, identity, plan)
	if err != nil {
		return err
	}
	if stage == recoveryStageNotStarted {
		return recoveryMarkerNotActive()
	}
	if err := clients.validateCommittedFederatedIdentityCredential(
		ctx,
		identity,
		plan,
		"before recovery commit",
	); err != nil {
		return err
	}
	if stage == recoveryStageCommittedFenced || stage == recoveryStageComplete {
		return nil
	}

	tags := mergeTags(uami.Tags, map[string]*string{
		workloadIdentityUIDTag:             to.Ptr(string(identity.UID)),
		workloadIdentityLastRecoveryUIDTag: to.Ptr(string(recovery.UID)),
	})
	if err := clients.updateRecoveryTags(ctx, identity, tags); err != nil {
		return fmt.Errorf("commit user assigned identity recovery: %w", err)
	}
	if err := clients.verifyRecoveryStage(
		ctx,
		recovery,
		identity,
		plan,
		recoveryStageCommittedFenced,
		"verify user assigned identity recovery commit",
	); err != nil {
		return err
	}
	return clients.validateCommittedFederatedIdentityCredential(
		ctx,
		identity,
		plan,
		"after recovery commit",
	)
}

// Finalize clears the Azure recovery fence after CommitVerified is durable.
func (m *WorkloadIdentityRecoveryManager) Finalize(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	clients, uami, stage, err := m.loadAndValidateRecoveryState(ctx, recovery, identity, plan)
	if err != nil {
		return err
	}
	if stage != recoveryStageCommittedFenced && stage != recoveryStageComplete {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonRecoveryMarkerConflict,
			"UserAssignedIdentity ownership is not committed",
		)
	}
	if err := clients.validateCommittedFederatedIdentityCredential(
		ctx,
		identity,
		plan,
		"before recovery finalization",
	); err != nil {
		return err
	}
	if stage == recoveryStageComplete {
		return nil
	}

	tags := mergeTags(uami.Tags, nil)
	delete(tags, workloadIdentityRecoveryUIDTag)
	delete(tags, workloadIdentityRecoveryTargetUIDTag)
	if err := clients.updateRecoveryTags(ctx, identity, tags); err != nil {
		return fmt.Errorf("finalize user assigned identity recovery: %w", err)
	}
	if err := clients.verifyRecoveryStage(
		ctx,
		recovery,
		identity,
		plan,
		recoveryStageComplete,
		"verify user assigned identity recovery finalization",
	); err != nil {
		return err
	}
	return clients.validateCommittedFederatedIdentityCredential(
		ctx,
		identity,
		plan,
		"after recovery finalization",
	)
}

type identityRecoveryStage int

const (
	recoveryStageNotStarted identityRecoveryStage = iota
	recoveryStageInProgress
	recoveryStageCommittedFenced
	recoveryStageComplete
)

func recoveryStage(
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	uami armmsi.Identity,
) (identityRecoveryStage, error) {
	ownerUID := tagValue(uami.Tags, workloadIdentityUIDTag)
	recoveryUID := tagValue(uami.Tags, workloadIdentityRecoveryUIDTag)
	targetUID := tagValue(uami.Tags, workloadIdentityRecoveryTargetUIDTag)
	lastRecoveryUID := tagValue(uami.Tags, workloadIdentityLastRecoveryUIDTag)
	sourceUID := string(recovery.Spec.PreviousWorkloadIdentityUID)
	currentUID := string(identity.UID)

	switch {
	case ownerUID == currentUID &&
		recoveryUID == "" &&
		targetUID == "" &&
		lastRecoveryUID == string(recovery.UID):
		return recoveryStageComplete, nil
	case ownerUID == currentUID &&
		recoveryUID == string(recovery.UID) &&
		targetUID == currentUID &&
		lastRecoveryUID == string(recovery.UID):
		return recoveryStageCommittedFenced, nil
	case ownerUID == sourceUID &&
		recoveryUID == string(recovery.UID) &&
		targetUID == currentUID:
		return recoveryStageInProgress, nil
	case ownerUID == sourceUID && recoveryUID == "" && targetUID == "":
		return recoveryStageNotStarted, nil
	default:
		return recoveryStageNotStarted, workloadidentity.NewRecoveryBlockedError(
			recoveryReasonRecoveryMarkerConflict,
			fmt.Sprintf(
				"UserAssignedIdentity %q has owner UID %q, recovery UID %q, target UID %q, and last recovery UID %q",
				userAssignedIdentityName(identity),
				ownerUID,
				recoveryUID,
				targetUID,
				lastRecoveryUID,
			),
		)
	}
}

func (c *identityClients) getRecoveryUserAssignedIdentity(
	ctx context.Context,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) (armmsi.Identity, error) {
	name := userAssignedIdentityName(identity)
	response, err := c.identities.Get(ctx, c.scope.resourceGroupName, name, nil)
	if isNotFound(err) {
		return armmsi.Identity{}, workloadidentity.NewRecoveryBlockedError(
			recoveryReasonUserAssignedIdentityNotFound,
			fmt.Sprintf("UserAssignedIdentity %q was not found", name),
		)
	}
	if err != nil {
		return armmsi.Identity{}, fmt.Errorf("get user assigned identity for recovery: %w", err)
	}
	expectedID := desiredUserAssignedIdentityID(c.scope, identity)
	if !strings.EqualFold(stringValue(response.ID), expectedID) {
		return armmsi.Identity{}, workloadidentity.NewRecoveryBlockedError(
			recoveryReasonUserAssignedIdentityConflict,
			fmt.Sprintf(
				"UserAssignedIdentity %q has resource ID %q; expected %q",
				name,
				stringValue(response.ID),
				expectedID,
			),
		)
	}
	currentOwnerUID := tagValue(response.Tags, workloadIdentityUIDTag)
	expectedTags := operatorOwnershipTags(workloadIdentityUIDTag, currentOwnerUID, true)
	expectedTags[workloadIdentityKeyTag] = to.Ptr(
		workloadidentity.LogicalIdentityKey(identity.Namespace, identity.Name),
	)
	if !hasTags(response.Tags, expectedTags) {
		return armmsi.Identity{}, workloadidentity.NewRecoveryBlockedError(
			recoveryReasonUserAssignedIdentityConflict,
			fmt.Sprintf("UserAssignedIdentity %q does not contain exact operator ownership tags", name),
		)
	}
	if response.Properties == nil ||
		stringValue(response.Properties.ClientID) == "" ||
		stringValue(response.Properties.PrincipalID) == "" ||
		stringValue(response.Properties.TenantID) == "" {
		return armmsi.Identity{}, workloadidentity.NewRecoveryBlockedError(
			recoveryReasonUserAssignedIdentityConflict,
			fmt.Sprintf("UserAssignedIdentity %q does not contain complete Azure identity properties", name),
		)
	}
	return response.Identity, nil
}

func (c *identityClients) updateRecoveryTags(
	ctx context.Context,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	tags map[string]*string,
) error {
	_, err := c.identities.Update(
		ctx,
		c.scope.resourceGroupName,
		userAssignedIdentityName(identity),
		armmsi.IdentityUpdate{Tags: tags},
		nil,
	)
	return err
}

func (c *identityClients) verifyRecoveryStage(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
	expected identityRecoveryStage,
	operation string,
) error {
	verified, err := c.getRecoveryUserAssignedIdentity(ctx, identity)
	if err != nil {
		return err
	}
	stage, err := recoveryStage(recovery, identity, verified)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if stage != expected {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonRecoveryMarkerConflict,
			fmt.Sprintf("%s did not reach the expected Azure recovery stage", operation),
		)
	}
	return validateRecoveryIdentityPlan(verified, plan)
}

func (c *identityClients) validateCommittedFederatedIdentityCredential(
	ctx context.Context,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
	operation string,
) error {
	credentials, err := c.federatedCredentials.List(
		ctx,
		c.scope.resourceGroupName,
		userAssignedIdentityName(identity),
	)
	if err != nil {
		return fmt.Errorf("list federated identity credentials %s: %w", operation, err)
	}
	return validateExactFederatedIdentityCredentialSet(
		credentials,
		identity.Spec.Azure.FederatedIdentityCredentialName,
		desiredFederatedIdentityCredentialID(c.scope, identity),
		desiredRecoveryFederatedIdentityCredential(plan),
	)
}

func validateRecoverableFederatedIdentityCredentialSet(
	scope Scope,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	credentials []armmsi.FederatedIdentityCredential,
) error {
	if len(credentials) > 1 {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonFederatedIdentityCredentialAmbiguous,
			fmt.Sprintf(
				"UserAssignedIdentity %q contains %d federated identity credentials; recovery requires zero or one",
				userAssignedIdentityName(identity),
				len(credentials),
			),
		)
	}
	if len(credentials) == 0 {
		return nil
	}
	credential := credentials[0]
	expectedName := identity.Spec.Azure.FederatedIdentityCredentialName
	expectedID := desiredFederatedIdentityCredentialID(scope, identity)
	return validateFederatedIdentityCredentialLocation(credential, expectedName, expectedID)
}

func validateExactFederatedIdentityCredentialSet(
	credentials []armmsi.FederatedIdentityCredential,
	expectedName, expectedID string,
	expected armmsi.FederatedIdentityCredential,
) error {
	if len(credentials) != 1 {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonFederatedIdentityCredentialAmbiguous,
			fmt.Sprintf(
				"Recovery requires exactly one federated identity credential, found %d",
				len(credentials),
			),
		)
	}
	current := credentials[0]
	if err := validateFederatedIdentityCredentialLocation(current, expectedName, expectedID); err != nil {
		return err
	}
	if err := validateFederatedIdentityCredential(expectedName, current, expected); err != nil {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonFederatedIdentityCredentialAmbiguous,
			fmt.Sprintf("FederatedIdentityCredential tuple changed: %v", err),
		)
	}
	return nil
}

func validateFederatedIdentityCredentialLocation(
	credential armmsi.FederatedIdentityCredential,
	expectedName, expectedID string,
) error {
	if !strings.EqualFold(stringValue(credential.Name), expectedName) ||
		!strings.EqualFold(stringValue(credential.ID), expectedID) {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonFederatedIdentityCredentialAmbiguous,
			fmt.Sprintf(
				"FederatedIdentityCredential has name %q and resource ID %q; expected name %q and resource ID %q",
				stringValue(credential.Name),
				stringValue(credential.ID),
				expectedName,
				expectedID,
			),
		)
	}
	return nil
}

func recoveryUserAssignedIdentity(
	identity armmsi.Identity,
) workloadidentityv1alpha1.WorkloadIdentityRecoveryUserAssignedIdentity {
	return workloadidentityv1alpha1.WorkloadIdentityRecoveryUserAssignedIdentity{
		ID:          stringValue(identity.ID),
		ClientID:    stringValue(identity.Properties.ClientID),
		PrincipalID: stringValue(identity.Properties.PrincipalID),
		TenantID:    stringValue(identity.Properties.TenantID),
	}
}

func desiredRecoveryFederatedIdentityCredential(
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) armmsi.FederatedIdentityCredential {
	desired := plan.FederatedIdentityCredential
	return armmsi.FederatedIdentityCredential{
		Properties: &armmsi.FederatedIdentityCredentialProperties{
			Issuer:    to.Ptr(desired.Issuer),
			Subject:   to.Ptr(desired.Subject),
			Audiences: stringPointers(desired.Audiences),
		},
	}
}

func validateRecoveryIdentityPlan(
	current armmsi.Identity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	if plan == nil {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonUserAssignedIdentityConflict,
			"Recovery plan is missing",
		)
	}
	expected := plan.UserAssignedIdentity
	if !strings.EqualFold(stringValue(current.ID), expected.ID) ||
		stringValue(current.Properties.ClientID) != expected.ClientID ||
		stringValue(current.Properties.PrincipalID) != expected.PrincipalID ||
		stringValue(current.Properties.TenantID) != expected.TenantID {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonUserAssignedIdentityConflict,
			"UserAssignedIdentity properties changed after recovery preflight",
		)
	}
	return nil
}

func recoveryMarkerNotActive() error {
	return workloadidentity.NewRecoveryBlockedError(
		recoveryReasonRecoveryMarkerConflict,
		"UserAssignedIdentity recovery marker is not active",
	)
}

func desiredFederatedIdentityCredentialID(
	scope Scope,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) string {
	return fmt.Sprintf(
		"%s/federatedIdentityCredentials/%s",
		desiredUserAssignedIdentityID(scope, identity),
		identity.Spec.Azure.FederatedIdentityCredentialName,
	)
}

func stringPointers(values []string) []*string {
	result := make([]*string, 0, len(values))
	for _, value := range values {
		result = append(result, to.Ptr(value))
	}
	return result
}
