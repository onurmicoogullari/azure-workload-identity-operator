/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"errors"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/azure"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

type fakeHealthCheckRegistrar struct {
	healthChecks map[string]healthz.Checker
	readyChecks  map[string]healthz.Checker
	addError     error
}

func (f *fakeHealthCheckRegistrar) AddHealthzCheck(name string, check healthz.Checker) error {
	if f.addError != nil {
		return f.addError
	}
	f.healthChecks[name] = check
	return nil
}

func (f *fakeHealthCheckRegistrar) AddReadyzCheck(name string, check healthz.Checker) error {
	if f.addError != nil {
		return f.addError
	}
	f.readyChecks[name] = check
	return nil
}

func newFakeHealthCheckRegistrar() *fakeHealthCheckRegistrar {
	return &fakeHealthCheckRegistrar{
		healthChecks: make(map[string]healthz.Checker),
		readyChecks:  make(map[string]healthz.Checker),
	}
}

func TestOIDCIssuerRefreshIntervalFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want time.Duration
	}{
		{
			name: "default",
			want: controller.DefaultOIDCIssuerRefreshInterval,
		},
		{
			name: "new flag",
			args: []string{"--oidc-issuer-refresh-interval=2m"},
			want: 2 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
			flags.SetOutput(io.Discard)

			var interval time.Duration
			registerOIDCIssuerRefreshIntervalFlags(flags, &interval)

			if err := flags.Parse(tt.args); err != nil {
				t.Fatalf("parse flags: %v", err)
			}

			if interval != tt.want {
				t.Fatalf("interval = %s, want %s", interval, tt.want)
			}
		})
	}
}

func TestAzureScopeFlagsAreRequiredAndProduceValidatedScope(t *testing.T) {
	t.Run("defaults are rejected", func(t *testing.T) {
		flags := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var values azureScopeFlagValues
		registerAzureScopeFlags(flags, &values)
		if err := flags.Parse(nil); err != nil {
			t.Fatal(err)
		}

		if _, err := azure.NewScope(values.subscriptionID, values.resourceGroupName, values.location); err == nil {
			t.Fatal("expected empty Azure scope flags to be rejected")
		}
	})

	t.Run("configured values are accepted", func(t *testing.T) {
		const (
			subscriptionID    = "00000000-0000-0000-0000-000000000000"
			resourceGroupName = "rg-platform"
			location          = "swedencentral"
		)
		flags := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var values azureScopeFlagValues
		registerAzureScopeFlags(flags, &values)
		err := flags.Parse([]string{
			"--azure-subscription-id=" + subscriptionID,
			"--azure-resource-group-name=" + resourceGroupName,
			"--azure-location=" + location,
		})
		if err != nil {
			t.Fatal(err)
		}

		if values.subscriptionID != subscriptionID {
			t.Fatalf("subscription ID = %q", values.subscriptionID)
		}
		if values.resourceGroupName != resourceGroupName {
			t.Fatalf("resource group = %q", values.resourceGroupName)
		}
		if values.location != location {
			t.Fatalf("location = %q", values.location)
		}
		if _, err := azure.NewScope(values.subscriptionID, values.resourceGroupName, values.location); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRegisterHealthChecks(t *testing.T) {
	t.Run("registers webhook readiness when enabled", func(t *testing.T) {
		registrar := newFakeHealthCheckRegistrar()

		if err := registerHealthChecks(registrar, healthz.Ping); err != nil {
			t.Fatal(err)
		}

		if _, ok := registrar.healthChecks["healthz"]; !ok {
			t.Fatal("health check was not registered")
		}
		if _, ok := registrar.readyChecks["readyz"]; !ok {
			t.Fatal("base readiness check was not registered")
		}
		if _, ok := registrar.readyChecks["webhook"]; !ok {
			t.Fatal("webhook readiness check was not registered")
		}
	})

	t.Run("omits webhook readiness when disabled", func(t *testing.T) {
		registrar := newFakeHealthCheckRegistrar()

		if err := registerHealthChecks(registrar, nil); err != nil {
			t.Fatal(err)
		}

		if _, ok := registrar.readyChecks["webhook"]; ok {
			t.Fatal("webhook readiness check was registered while webhooks were disabled")
		}
	})

	t.Run("returns registration errors", func(t *testing.T) {
		wantErr := errors.New("registration failed")
		registrar := newFakeHealthCheckRegistrar()
		registrar.addError = wantErr

		if err := registerHealthChecks(registrar, nil); !errors.Is(err, wantErr) {
			t.Fatalf("registerHealthChecks() error = %v, want %v", err, wantErr)
		}
	})
}
