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

package agent

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenDependencies are packages that could give the agent a way to read or change the cluster,
// or to reach a credential, without going through the broker or the injected model. The agent must not depend on any of
// them, directly or transitively (ADR-017, ADR-018).
var forbiddenDependencies = []string{
	"k8s.io/client-go",
	"sigs.k8s.io/controller-runtime",
	"github.com/adityax25/KubeCure/internal/tools",
	"github.com/adityax25/KubeCure/internal/evidence",
	"github.com/adityax25/KubeCure/internal/model",
	"google.golang.org/genai",
	"os/exec",
}

// TestAgentHoldsNoClient fails the build if the agent package acquires a dependency that could
// bypass the broker. Isolation is enforced here rather than maintained by convention, so a change
// that quietly hands the agent a client is caught by CI.
func TestAgentHoldsNoClient(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the go toolchain")
	}

	out, err := exec.Command("go", "list", "-deps", "github.com/adityax25/KubeCure/internal/agent").Output()
	if err != nil {
		t.Fatalf("listing dependencies: %v", err)
	}

	for _, dep := range strings.Fields(string(out)) {
		for _, forbidden := range forbiddenDependencies {
			if dep == forbidden || strings.HasPrefix(dep, forbidden+"/") {
				t.Errorf("internal/agent depends on %s, which could bypass the tool broker", dep)
			}
		}
	}
}
