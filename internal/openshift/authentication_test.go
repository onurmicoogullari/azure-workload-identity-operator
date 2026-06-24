package openshift

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUpdateServiceAccountIssuer(t *testing.T) {
	c := fakeOpenShiftClient(t, &configv1.Authentication{ObjectMeta: metav1.ObjectMeta{Name: authenticationName}})
	updater := &ServiceAccountIssuerUpdater{Client: c}

	if err := updater.UpdateServiceAccountIssuer(context.Background(), "https://issuer.example"); err != nil {
		t.Fatal(err)
	}

	authentication := &configv1.Authentication{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: authenticationName}, authentication); err != nil {
		t.Fatal(err)
	}
	if authentication.Spec.ServiceAccountIssuer != "https://issuer.example" {
		t.Fatalf("serviceAccountIssuer = %q", authentication.Spec.ServiceAccountIssuer)
	}
}

func TestUpdateServiceAccountIssuerNoopWhenCurrent(t *testing.T) {
	c := fakeOpenShiftClient(t, &configv1.Authentication{
		ObjectMeta: metav1.ObjectMeta{Name: authenticationName},
		Spec:       configv1.AuthenticationSpec{ServiceAccountIssuer: "https://issuer.example"},
	})
	updater := &ServiceAccountIssuerUpdater{Client: c}

	if err := updater.UpdateServiceAccountIssuer(context.Background(), "https://issuer.example"); err != nil {
		t.Fatal(err)
	}
}

func fakeOpenShiftClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
