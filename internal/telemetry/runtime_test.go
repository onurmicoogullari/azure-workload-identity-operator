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

package telemetry

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

func TestSamplerFromEnvironment(t *testing.T) {
	tests := []struct {
		name        string
		sampler     string
		argument    string
		description string
	}{
		{name: "default", description: "ParentBased{root:AlwaysOnSampler"},
		{name: "ratio", sampler: parentBasedTraceIDRatioSampler, argument: "0.25", description: "TraceIDRatioBased{0.25}"},
		{name: "off", sampler: "always_off", description: "AlwaysOffSampler"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER", test.sampler)
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", test.argument)
			sampler, err := samplerFromEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sampler.Description(), test.description) {
				t.Fatalf("description = %q, want substring %q", sampler.Description(), test.description)
			}
		})
	}
}

func TestInvalidSamplerDisablesTracingWithoutStartupFailure(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "not-a-sampler")
	ctx := logr.NewContext(context.Background(), logr.Discard())
	runtime, err := New(ctx, Config{Enabled: true})
	if err == nil {
		t.Fatal("expected invalid sampler error")
	}
	if runtime.Enabled() {
		t.Fatal("runtime remained enabled after invalid configuration")
	}
}
