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

package gemini

import (
	"encoding/json"
	"testing"

	"google.golang.org/genai"

	"github.com/adityax25/KubeCure/internal/agent"
)

func TestModelTurnIsReplayedVerbatim(t *testing.T) {
	signed := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
		FunctionCall:     &genai.FunctionCall{Name: "get_pod_logs", Args: map[string]any{}},
		ThoughtSignature: []byte("opaque-signature"),
	}}}

	contents, err := toContents([]agent.Message{
		{Role: agent.RoleUser, Text: "investigate"},
		{Role: agent.RoleModel, ToolCalls: []agent.ToolCall{{Name: "get_pod_logs"}}, ProviderState: signed},
		{Role: agent.RoleUser, ToolResults: []agent.ToolResult{{Name: "get_pod_logs", Content: "boom", IsError: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if contents[1] != signed {
		t.Fatal("the model turn was rebuilt instead of replayed, which would drop its thought signature")
	}
	resp := contents[2].Parts[0].FunctionResponse
	if resp == nil || resp.Name != "get_pod_logs" || resp.Response["error"] != "boom" {
		t.Fatalf("expected an error function response, got %+v", resp)
	}
}

func TestDeclarationsCarryTheSchema(t *testing.T) {
	decls, err := toDeclarations([]agent.ToolSpec{{
		Name:        "get_pod_logs",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"previous":{"type":"boolean"}}}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	schema, ok := decls[0].ParametersJsonSchema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("expected the JSON schema to be forwarded, got %#v", decls[0].ParametersJsonSchema)
	}
}

func TestThinkingTokensCountAsOutput(t *testing.T) {
	u := usageOf(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 100, CandidatesTokenCount: 20, ThoughtsTokenCount: 30,
	}})
	if u.InputTokens != 100 || u.OutputTokens != 50 {
		t.Fatalf("expected 100 in and 50 out, got %+v", u)
	}
}
