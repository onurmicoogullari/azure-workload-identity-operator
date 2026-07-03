package openshift

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const testServiceAccountIssuer = "https://issuer.example"

func TestServiceAccountIssuerClientSet(t *testing.T) {
	c := fakeOpenShiftClient(t, &configv1.Authentication{ObjectMeta: metav1.ObjectMeta{Name: authenticationName}})
	issuer := &ServiceAccountIssuerClient{Client: c}

	changed, err := issuer.Set(context.Background(), testServiceAccountIssuer)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}

	authentication := &configv1.Authentication{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: authenticationName}, authentication); err != nil {
		t.Fatal(err)
	}
	if authentication.Spec.ServiceAccountIssuer != testServiceAccountIssuer {
		t.Fatalf("serviceAccountIssuer = %q", authentication.Spec.ServiceAccountIssuer)
	}
}

func TestServiceAccountIssuerClientSetNoopWhenCurrent(t *testing.T) {
	patches := 0
	c := fakeOpenShiftClientWithInterceptor(t, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches++
			return c.Patch(ctx, obj, patch, opts...)
		},
	}, &configv1.Authentication{
		ObjectMeta: metav1.ObjectMeta{Name: authenticationName},
		Spec:       configv1.AuthenticationSpec{ServiceAccountIssuer: testServiceAccountIssuer},
	})
	issuer := &ServiceAccountIssuerClient{Client: c}

	changed, err := issuer.Set(context.Background(), testServiceAccountIssuer)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
	if patches != 0 {
		t.Fatalf("patches = %d, want 0", patches)
	}
}

func TestServiceAccountIssuerClientGet(t *testing.T) {
	c := fakeOpenShiftClient(t, &configv1.Authentication{
		ObjectMeta: metav1.ObjectMeta{Name: authenticationName},
		Spec:       configv1.AuthenticationSpec{ServiceAccountIssuer: testServiceAccountIssuer},
	})
	issuer := &ServiceAccountIssuerClient{Client: c}

	got, err := issuer.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != testServiceAccountIssuer {
		t.Fatalf("serviceAccountIssuer = %q", got)
	}
}

func TestAuthenticationAPIAvailable(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{configv1.GroupVersion})
	mapper.Add(configv1.GroupVersion.WithKind("Authentication"), meta.RESTScopeRoot)

	available, err := AuthenticationAPIAvailable(mapper)
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("available = false, want true")
	}
}

func TestAuthenticationAPIAvailableWhenMissing(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)

	available, err := AuthenticationAPIAvailable(mapper)
	if err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("available = true, want false")
	}
}

func TestServiceAccountIssuerClientWaitForKubeAPIServerRollout(t *testing.T) {
	changedAfter := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)
	rolledOutAt := metav1.NewTime(changedAfter.Add(time.Minute))
	operators := make([]client.Object, 0, 1+len(serviceAccountIssuerDependentOperators))
	operators = append(operators, healthyClusterOperator("kube-apiserver", rolledOutAt))
	for _, name := range serviceAccountIssuerDependentOperators {
		operators = append(operators, healthyClusterOperator(name, metav1.NewTime(changedAfter.Add(-time.Hour))))
	}
	c := fakeOpenShiftClient(t, operators...)
	issuer := &ServiceAccountIssuerClient{
		Client:              c,
		RolloutPollInterval: time.Millisecond,
		RolloutTimeout:      time.Second,
	}

	if err := issuer.WaitForKubeAPIServerRollout(context.Background(), changedAfter); err != nil {
		t.Fatal(err)
	}
}

func TestClusterOperatorHealthyAfter(t *testing.T) {
	changedAfter := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)
	afterChange := metav1.NewTime(changedAfter.Add(time.Minute))
	beforeChange := metav1.NewTime(changedAfter.Add(-time.Minute))

	tests := []struct {
		name       string
		conditions []configv1.ClusterOperatorStatusCondition
		want       bool
	}{
		{
			name: "ready after issuer change",
			conditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorCondition(configv1.OperatorAvailable, configv1.ConditionTrue, afterChange),
				newClusterOperatorCondition(configv1.OperatorProgressing, configv1.ConditionFalse, afterChange),
				newClusterOperatorCondition(configv1.OperatorDegraded, configv1.ConditionFalse, afterChange),
			},
			want: true,
		},
		{
			name: "progressing condition is stale",
			conditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorCondition(configv1.OperatorAvailable, configv1.ConditionTrue, afterChange),
				newClusterOperatorCondition(configv1.OperatorProgressing, configv1.ConditionFalse, beforeChange),
				newClusterOperatorCondition(configv1.OperatorDegraded, configv1.ConditionFalse, afterChange),
			},
			want: false,
		},
		{
			name: "still progressing",
			conditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorCondition(configv1.OperatorAvailable, configv1.ConditionTrue, afterChange),
				newClusterOperatorCondition(configv1.OperatorProgressing, configv1.ConditionTrue, afterChange),
				newClusterOperatorCondition(configv1.OperatorDegraded, configv1.ConditionFalse, afterChange),
			},
			want: false,
		},
		{
			name: "degraded",
			conditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorCondition(configv1.OperatorAvailable, configv1.ConditionTrue, afterChange),
				newClusterOperatorCondition(configv1.OperatorProgressing, configv1.ConditionFalse, afterChange),
				newClusterOperatorCondition(configv1.OperatorDegraded, configv1.ConditionTrue, afterChange),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operator := &configv1.ClusterOperator{
				Status: configv1.ClusterOperatorStatus{
					Conditions: tt.conditions,
				},
			}
			if got := clusterOperatorHealthyAfter(operator, changedAfter); got != tt.want {
				t.Fatalf("clusterOperatorHealthyAfter() = %t, want %t", got, tt.want)
			}
		})
	}
}

func healthyClusterOperator(name string, rolledOutAt metav1.Time) *configv1.ClusterOperator {
	return &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: configv1.ClusterOperatorStatus{
			Conditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorCondition(configv1.OperatorAvailable, configv1.ConditionTrue, rolledOutAt),
				newClusterOperatorCondition(configv1.OperatorProgressing, configv1.ConditionFalse, rolledOutAt),
				newClusterOperatorCondition(configv1.OperatorDegraded, configv1.ConditionFalse, rolledOutAt),
			},
		},
	}
}

func fakeOpenShiftClient(t *testing.T, objects ...client.Object) client.Client {
	return fakeOpenShiftClientWithInterceptor(t, interceptor.Funcs{}, objects...)
}

func fakeOpenShiftClientWithInterceptor(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(funcs).Build()
}

func newClusterOperatorCondition(conditionType configv1.ClusterStatusConditionType, status configv1.ConditionStatus, lastTransitionTime metav1.Time) configv1.ClusterOperatorStatusCondition {
	return configv1.ClusterOperatorStatusCondition{
		Type:               conditionType,
		Status:             status,
		LastTransitionTime: lastTransitionTime,
	}
}
