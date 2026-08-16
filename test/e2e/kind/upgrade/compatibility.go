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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/yaml"
)

var fullCommitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type crdVersion struct {
	Name    string `json:"name"`
	Served  bool   `json:"served"`
	Storage bool   `json:"storage"`
}

type crdSpec struct {
	Versions []crdVersion `json:"versions"`
}

type crdState struct {
	Name             string
	UID              string
	ReleaseName      string
	ReleaseNamespace string
	Spec             json.RawMessage
	StoredVersions   []string
	Objects          map[string]crdObjectState
}

type crdObjectState struct {
	UID  string
	Spec json.RawMessage
}

func crdNamesFromManifest(manifest string) ([]string, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	seen := make(map[string]struct{})
	var names []string
	for {
		var document struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := decoder.Decode(&document); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode rendered Helm manifest: %w", err)
		}
		if document.Kind != "CustomResourceDefinition" ||
			!strings.HasPrefix(document.APIVersion, "apiextensions.k8s.io/") {
			continue
		}
		if document.Metadata.Name == "" {
			return nil, fmt.Errorf("rendered CustomResourceDefinition has no metadata.name")
		}
		if _, exists := seen[document.Metadata.Name]; exists {
			return nil, fmt.Errorf("rendered package repeats CRD %s", document.Metadata.Name)
		}
		seen[document.Metadata.Name] = struct{}{}
		names = append(names, document.Metadata.Name)
	}
	slices.Sort(names)
	return names, nil
}

func mergeCRDNames(inventories ...[]string) []string {
	seen := make(map[string]struct{})
	for _, inventory := range inventories {
		for _, name := range inventory {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func validateCommitID(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required and must be a full 40-character Git commit", name)
	}
	if !fullCommitPattern.MatchString(value) {
		return fmt.Errorf("%s=%q is not a full lowercase 40-character Git commit", name, value)
	}
	if strings.Trim(value, "0") == "" {
		return fmt.Errorf("%s is the all-zero Git sentinel and cannot identify a meaningful revision", name)
	}
	return nil
}

func validateDistinctCommits(baseline, candidate string) error {
	if baseline == candidate {
		return fmt.Errorf("baseline and candidate both resolve to %s; upgrade coverage requires distinct commits", baseline)
	}
	return nil
}

func validateCRDState(state crdState) error {
	var spec crdSpec
	if err := json.Unmarshal(state.Spec, &spec); err != nil {
		return fmt.Errorf("decode CRD %s spec: %w", state.Name, err)
	}
	if len(spec.Versions) == 0 {
		return fmt.Errorf("CRD %s has no versions", state.Name)
	}

	versions := make(map[string]crdVersion, len(spec.Versions))
	storageVersions := 0
	for _, version := range spec.Versions {
		if version.Name == "" {
			return fmt.Errorf("CRD %s has an unnamed version", state.Name)
		}
		if _, exists := versions[version.Name]; exists {
			return fmt.Errorf("CRD %s repeats version %s", state.Name, version.Name)
		}
		versions[version.Name] = version
		if version.Storage {
			storageVersions++
			if !version.Served {
				return fmt.Errorf("CRD %s storage version %s is not served", state.Name, version.Name)
			}
		}
	}
	if storageVersions != 1 {
		return fmt.Errorf("CRD %s has %d storage versions, want exactly one", state.Name, storageVersions)
	}
	if len(state.StoredVersions) == 0 {
		return fmt.Errorf("CRD %s has no status.storedVersions", state.Name)
	}
	for _, stored := range state.StoredVersions {
		if _, exists := versions[stored]; !exists {
			return fmt.Errorf("CRD %s stored version %s is absent from spec.versions", state.Name, stored)
		}
	}
	return nil
}

// rollbackCompatibility proves that the baseline controller can be restored
// without downgrading a baseline CRD or abandoning candidate-only objects. A
// candidate-only, empty CRD is safe to retain because Helm does not need to
// remove it when the baseline controller is restored.
func rollbackCompatibility(
	baselineNames []string,
	candidateNames []string,
	baselineStates map[string]crdState,
	candidateStates map[string]crdState,
) (bool, []string) {
	baselineInventory := make(map[string]struct{}, len(baselineNames))
	for _, name := range baselineNames {
		baselineInventory[name] = struct{}{}
	}
	var reasons []string
	for _, name := range mergeCRDNames(baselineNames, candidateNames) {
		baselineState, baselineStateExists := baselineStates[name]
		candidateState, candidateStateExists := candidateStates[name]
		if _, existedInBaseline := baselineInventory[name]; existedInBaseline {
			if !baselineStateExists {
				reasons = append(reasons, fmt.Sprintf("baseline CRD %s was not captured", name))
				continue
			}
			if !candidateStateExists {
				reasons = append(reasons, fmt.Sprintf("baseline CRD %s was not retained by the candidate", name))
				continue
			}
			if baselineState.UID != candidateState.UID {
				reasons = append(reasons, fmt.Sprintf("baseline CRD %s UID changed", name))
			}
			if !bytes.Equal(canonicalJSON(baselineState.Spec), canonicalJSON(candidateState.Spec)) {
				reasons = append(reasons, fmt.Sprintf("baseline CRD %s spec changed", name))
			}
			reasons = append(reasons, compareCRDObjects(name, baselineState.Objects, candidateState.Objects)...)
			continue
		}
		if !candidateStateExists {
			reasons = append(reasons, fmt.Sprintf("candidate CRD %s was not installed", name))
			continue
		}
		if len(candidateState.Objects) != 0 {
			reasons = append(reasons, fmt.Sprintf(
				"candidate-only CRD %s has %d stored objects", name, len(candidateState.Objects)))
		}
	}
	return len(reasons) == 0, reasons
}

func compareCRDObjects(name string, baseline, candidate map[string]crdObjectState) []string {
	var reasons []string
	for _, objectName := range mergeObjectNames(baseline, candidate) {
		baselineObject, baselineExists := baseline[objectName]
		candidateObject, candidateExists := candidate[objectName]
		switch {
		case !baselineExists:
			reasons = append(reasons, fmt.Sprintf("baseline CRD %s gained object %s", name, objectName))
		case !candidateExists:
			reasons = append(reasons, fmt.Sprintf("baseline CRD %s lost object %s", name, objectName))
		case baselineObject.UID != candidateObject.UID:
			reasons = append(reasons, fmt.Sprintf("baseline CRD %s object %s UID changed", name, objectName))
		case !bytes.Equal(canonicalJSON(baselineObject.Spec), canonicalJSON(candidateObject.Spec)):
			reasons = append(reasons, fmt.Sprintf("baseline CRD %s object %s spec changed", name, objectName))
		}
	}
	return reasons
}

func mergeObjectNames(inventories ...map[string]crdObjectState) []string {
	seen := make(map[string]struct{})
	for _, inventory := range inventories {
		for name := range inventory {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func canonicalJSON(raw json.RawMessage) []byte {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return canonical
}
