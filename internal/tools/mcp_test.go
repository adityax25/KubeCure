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

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPServesTheSameGuardedCatalogue connects a real MCP client to the server and confirms that
// the protocol surface exposes the broker's catalogue and enforces the broker's scope, rather than
// offering a path around it.
func TestMCPServesTheSameGuardedCatalogue(t *testing.T) {
	ctx := context.Background()
	b := newBroker(t, checkoutScope(), fixtures()...)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewMCPServer(b, "test").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}
	if len(listed.Tools) != len(b.Tools()) {
		t.Fatalf("MCP lists %d tools, broker has %d", len(listed.Tools), len(b.Tools()))
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not advertised as read only", tool.Name)
		}
	}

	// A permitted read returns a redacted result.
	ok, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_workload_spec", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("calling tool: %v", err)
	}
	if ok.IsError || strings.Contains(textOf(ok), "hunter2") {
		t.Fatalf("expected a redacted success, got error=%v body=%q", ok.IsError, textOf(ok))
	}

	// An out of scope read over MCP is refused exactly as it is in process.
	refused, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_pod_events",
		Arguments: map[string]any{"pod": "billing-7d9f8b6c4-x1"},
	})
	if err != nil {
		t.Fatalf("calling tool: %v", err)
	}
	if !refused.IsError || !strings.Contains(textOf(refused), "outside the investigated workload") {
		t.Fatalf("expected the MCP call to be denied, got %q", textOf(refused))
	}

	// Calls made over MCP land in the same audit record.
	if got := len(b.Calls()); got != 2 {
		t.Fatalf("expected 2 audited calls from the MCP session, got %d", got)
	}
}

func textOf(r *mcp.CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
