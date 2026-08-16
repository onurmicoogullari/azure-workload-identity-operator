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

package upgrade

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

const (
	testCRDName = "widgets.example.test"
	baselineUID = "baseline-uid"
)

func TestValidateCommitInputs(t *testing.T) {
	t.Parallel()

	baseline := strings.Repeat("a", 40)
	candidate := strings.Repeat("b", 40)
	for name, value := range map[string]string{
		"missing":   "",
		"short":     "abc123",
		"uppercase": strings.Repeat("A", 40),
		"all-zero":  strings.Repeat("0", 40),
	} {
		if err := validateCommitID(name, value); err == nil {
			t.Errorf("validateCommitID(%q, %q) succeeded", name, value)
		}
	}
	if err := validateCommitID("baseline", baseline); err != nil {
		t.Fatalf("valid baseline was rejected: %v", err)
	}
	if err := validateDistinctCommits(baseline, candidate); err != nil {
		t.Fatalf("distinct commits were rejected: %v", err)
	}
	if err := validateDistinctCommits(baseline, baseline); err == nil {
		t.Fatal("equal baseline and candidate were accepted")
	}
}

func TestValidateCRDState(t *testing.T) {
	t.Parallel()

	valid := crdState{
		Name:           testCRDName,
		Spec:           json.RawMessage(`{"versions":[{"name":"v1","served":true,"storage":true}]}`),
		StoredVersions: []string{"v1"},
	}
	if err := validateCRDState(valid); err != nil {
		t.Fatalf("valid CRD state was rejected: %v", err)
	}

	invalid := valid
	invalid.StoredVersions = []string{"v1alpha1"}
	if err := validateCRDState(invalid); err == nil || !strings.Contains(err.Error(), "absent from spec.versions") {
		t.Fatalf("validateCRDState() error = %v, want missing stored version", err)
	}
}

func TestCRDNamesFromManifest(t *testing.T) {
	t.Parallel()

	manifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: ignored
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: zeds.example.test
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: alphas.example.test
`
	names, err := crdNamesFromManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alphas.example.test", "zeds.example.test"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("CRD inventory = %v, want %v", names, want)
	}

	duplicate := manifest + `---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: alphas.example.test
`
	if _, err := crdNamesFromManifest(duplicate); err == nil || !strings.Contains(err.Error(), "repeats CRD") {
		t.Fatalf("duplicate CRD error = %v, want duplicate rejection", err)
	}
}

func TestRollbackCompatibility(t *testing.T) {
	t.Parallel()

	baselineNames := []string{testCRDName}
	baselineStates := map[string]crdState{
		testCRDName: {
			Name: testCRDName,
			UID:  baselineUID,
			Spec: json.RawMessage(`{
				"group":"example.test",
				"versions":[{"storage":true,"served":true,"name":"v1"}]
			}`),
			Objects: map[string]crdObjectState{
				"default/example": {UID: "object-uid", Spec: json.RawMessage(`{"value":"baseline"}`)},
			},
		},
	}
	equivalentStates := map[string]crdState{
		testCRDName: {
			Name: testCRDName,
			UID:  baselineUID,
			Spec: json.RawMessage(`{"versions":[{"name":"v1","served":true,"storage":true}],"group":"example.test"}`),
			Objects: map[string]crdObjectState{
				"default/example": {UID: "object-uid", Spec: json.RawMessage(`{"value":"baseline"}`)},
			},
		},
	}
	compatible, reasons := rollbackCompatibility(
		baselineNames, baselineNames, baselineStates, equivalentStates)
	if !compatible || len(reasons) != 0 {
		t.Fatalf("equivalent CRDs are incompatible: %v", reasons)
	}

	changedStates := map[string]crdState{
		testCRDName: {
			Name: testCRDName,
			UID:  baselineUID,
			Spec: json.RawMessage(`{
				"group":"example.test",
				"versions":[{"name":"v1","served":true,"storage":true}],
				"preserveUnknownFields":false
			}`),
			Objects: baselineStates[testCRDName].Objects,
		},
	}
	compatible, reasons = rollbackCompatibility(
		baselineNames, baselineNames, baselineStates, changedStates)
	if compatible || len(reasons) != 1 || !strings.Contains(reasons[0], "spec changed") {
		t.Fatalf("changed CRD compatibility = %v, %v; want roll-forward-only", compatible, reasons)
	}

	const addedCRDName = "additions.example.test"
	additionStates := map[string]crdState{
		testCRDName:  equivalentStates[testCRDName],
		addedCRDName: {Name: addedCRDName, UID: "added-uid"},
	}
	compatible, reasons = rollbackCompatibility(
		baselineNames, []string{testCRDName, addedCRDName}, baselineStates, additionStates)
	if !compatible || len(reasons) != 0 {
		t.Fatalf("empty candidate-only CRD blocked rollback: %v", reasons)
	}

	additionWithObjects := maps.Clone(additionStates)
	addedWithObject := additionWithObjects[addedCRDName]
	addedWithObject.Objects = map[string]crdObjectState{
		"default/new": {UID: "new-object-uid"},
	}
	additionWithObjects[addedCRDName] = addedWithObject
	compatible, reasons = rollbackCompatibility(
		baselineNames, []string{testCRDName, addedCRDName}, baselineStates, additionWithObjects)
	if compatible || len(reasons) != 1 || !strings.Contains(reasons[0], "has 1 stored objects") {
		t.Fatalf("populated candidate-only CRD compatibility = %v, %v; want roll-forward-only", compatible, reasons)
	}

	mutatedObjectStates := maps.Clone(equivalentStates)
	mutatedCRD := mutatedObjectStates[testCRDName]
	mutatedCRD.Objects = maps.Clone(mutatedCRD.Objects)
	mutatedObject := mutatedCRD.Objects["default/example"]
	mutatedObject.Spec = json.RawMessage(`{"value":"candidate"}`)
	mutatedCRD.Objects["default/example"] = mutatedObject
	mutatedObjectStates[testCRDName] = mutatedCRD
	compatible, reasons = rollbackCompatibility(
		baselineNames, baselineNames, baselineStates, mutatedObjectStates)
	if compatible || len(reasons) != 1 || !strings.Contains(reasons[0], "object default/example spec changed") {
		t.Fatalf("mutated baseline object compatibility = %v, %v; want roll-forward-only", compatible, reasons)
	}

	const removedCRDName = "removed.example.test"
	removedState := crdState{Name: removedCRDName, UID: "removed-uid", Spec: json.RawMessage(`{"versions":[]}`)}
	removalBaseline := maps.Clone(baselineStates)
	removalBaseline[removedCRDName] = removedState
	removalCandidate := maps.Clone(equivalentStates)
	removalCandidate[removedCRDName] = removedState
	compatible, reasons = rollbackCompatibility(
		[]string{testCRDName, removedCRDName}, baselineNames, removalBaseline, removalCandidate)
	if !compatible || len(reasons) != 0 {
		t.Fatalf("retained baseline-only CRD blocked rollback: %v", reasons)
	}
}
