package openshift

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	authenticationName               = "cluster"
	defaultRolloutPollInterval       = 15 * time.Second
	defaultServiceAccountWaitTimeout = 20 * time.Minute
)

var serviceAccountIssuerClusterOperators = []string{
	"authentication",
	"openshift-apiserver",
}

// ServiceAccountIssuerClient manages Authentication.spec.serviceAccountIssuer.
type ServiceAccountIssuerClient struct {
	Client              client.Client
	RolloutPollInterval time.Duration
	RolloutTimeout      time.Duration
}

// ServiceAccountIssuerReader reads Authentication.spec.serviceAccountIssuer.
type ServiceAccountIssuerReader struct {
	Reader client.Reader
}

func (c *ServiceAccountIssuerClient) Get(ctx context.Context) (string, error) {
	authentication, err := authentication(ctx, c.Client)
	if err != nil {
		return "", err
	}
	return authentication.Spec.ServiceAccountIssuer, nil
}

func (r *ServiceAccountIssuerReader) Get(ctx context.Context) (string, error) {
	authentication, err := authentication(ctx, r.Reader)
	if err != nil {
		return "", err
	}
	return authentication.Spec.ServiceAccountIssuer, nil
}

func (c *ServiceAccountIssuerClient) Set(ctx context.Context, issuerURL string) (bool, error) {
	authentication, err := authentication(ctx, c.Client)
	if err != nil {
		return false, err
	}
	if authentication.Spec.ServiceAccountIssuer == issuerURL {
		return false, nil
	}

	patch := client.MergeFrom(authentication.DeepCopy())
	authentication.Spec.ServiceAccountIssuer = issuerURL
	if err := c.Client.Patch(ctx, authentication, patch); err != nil {
		return false, fmt.Errorf("set OpenShift service account issuer: %w", err)
	}
	return true, nil
}

func AuthenticationAPIAvailable(mapper meta.RESTMapper) (bool, error) {
	_, err := mapper.RESTMapping(configv1.GroupVersion.WithKind("Authentication").GroupKind(), configv1.GroupVersion.Version)
	if err == nil {
		return true, nil
	}
	if meta.IsNoMatchError(err) {
		return false, nil
	}
	return false, err
}

func authentication(ctx context.Context, reader client.Reader) (*configv1.Authentication, error) {
	if reader == nil {
		return nil, fmt.Errorf("kubernetes reader is required")
	}

	authentication := &configv1.Authentication{}
	if err := reader.Get(ctx, client.ObjectKey{Name: authenticationName}, authentication); err != nil {
		return nil, fmt.Errorf("get OpenShift Authentication %q: %w", authenticationName, err)
	}
	return authentication, nil
}

func (c *ServiceAccountIssuerClient) WaitForKubeAPIServerRollout(ctx context.Context, changedAfter time.Time) error {
	if c.Client == nil {
		return fmt.Errorf("kubernetes client is required")
	}

	return wait.PollUntilContextTimeout(ctx, c.rolloutPollInterval(), c.rolloutTimeout(), true, func(ctx context.Context) (bool, error) {
		operator := &configv1.ClusterOperator{}
		if err := c.Client.Get(ctx, client.ObjectKey{Name: "kube-apiserver"}, operator); err != nil {
			return false, fmt.Errorf("get OpenShift ClusterOperator %q: %w", "kube-apiserver", err)
		}
		if !clusterOperatorHealthyAfter(operator, changedAfter) {
			return false, nil
		}

		for _, name := range serviceAccountIssuerClusterOperators {
			operator := &configv1.ClusterOperator{}
			if err := c.Client.Get(ctx, client.ObjectKey{Name: name}, operator); err != nil {
				return false, fmt.Errorf("get OpenShift ClusterOperator %q: %w", name, err)
			}
			if !clusterOperatorHealthy(operator) {
				return false, nil
			}
		}
		return true, nil
	})
}

func (c *ServiceAccountIssuerClient) rolloutPollInterval() time.Duration {
	if c.RolloutPollInterval > 0 {
		return c.RolloutPollInterval
	}
	return defaultRolloutPollInterval
}

func (c *ServiceAccountIssuerClient) rolloutTimeout() time.Duration {
	if c.RolloutTimeout > 0 {
		return c.RolloutTimeout
	}
	return defaultServiceAccountWaitTimeout
}

func clusterOperatorHealthyAfter(operator *configv1.ClusterOperator, changedAfter time.Time) bool {
	if !clusterOperatorHealthy(operator) {
		return false
	}
	progressing := clusterOperatorCondition(operator.Status.Conditions, configv1.OperatorProgressing)
	return progressing != nil && !progressing.LastTransitionTime.Time.Before(changedAfter)
}

func clusterOperatorHealthy(operator *configv1.ClusterOperator) bool {
	available := clusterOperatorCondition(operator.Status.Conditions, configv1.OperatorAvailable)
	progressing := clusterOperatorCondition(operator.Status.Conditions, configv1.OperatorProgressing)
	degraded := clusterOperatorCondition(operator.Status.Conditions, configv1.OperatorDegraded)
	return available != nil &&
		available.Status == configv1.ConditionTrue &&
		progressing != nil &&
		progressing.Status == configv1.ConditionFalse &&
		degraded != nil &&
		degraded.Status == configv1.ConditionFalse
}

func clusterOperatorCondition(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType) *configv1.ClusterOperatorStatusCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
