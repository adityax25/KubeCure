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

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPServer exposes a bound broker over the Model Context Protocol. An external client connected
// to it sees exactly the catalogue the investigation agent sees, and every call passes through the
// same scope checks, scrubbing, and audit. The protocol is an additional surface onto the broker,
// never a way around it.
func NewMCPServer(b *Broker, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "kubecure",
		Title:   "KubeCure investigation tools",
		Version: version,
	}, nil)

	notDestructive := false
	for _, spec := range b.Tools() {
		name := spec.Name
		server.AddTool(&mcp.Tool{
			Name:        name,
			Description: spec.Description,
			InputSchema: spec.InputSchema,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    true,
				IdempotentHint:  true,
				DestructiveHint: &notDestructive,
			},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := b.Call(ctx, name, req.Params.Arguments)
			if err != nil {
				// Tool failures are reported as error results rather than protocol errors, so the
				// client sees the reason, including a denial, instead of a broken session.
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
				}, nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: result}},
			}, nil
		})
	}
	return server
}
