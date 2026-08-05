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

package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectDirFindsModuleRoot(t *testing.T) {
	root, err := ProjectDir()
	if err != nil {
		t.Fatal(err)
	}

	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read project go.mod: %v", err)
	}
	if !strings.HasPrefix(string(goMod), "module github.com/onurmicoogullari/azure-workload-identity-operator\n") {
		t.Fatalf("ProjectDir() returned %q, which is not this module root", root)
	}
}
