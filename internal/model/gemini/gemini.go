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

// Package gemini adapts Google's Gemini API to the investigation loop's Model interface.
//
// This package is the only place the model API key exists. The agent package depends on the Model
// interface and never imports this one (ADR-018).
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/genai"

	"github.com/adityax25/KubeCure/internal/agent"
)

// Model calls one Gemini model.
type Model struct {
	client *genai.Client
	model  string
}

var _ agent.Model = (*Model)(nil)

// New builds a Gemini model client. The API key is held here and nowhere else.
func New(ctx context.Context, apiKey, model string) (*Model, error) {
	if apiKey == "" {
		return nil, errors.New("gemini: an API key is required")
	}
	if model == "" {
		return nil, errors.New("gemini: a model name is required")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, fmt.Errorf("gemini: building client: %w", err)
	}
	return &Model{client: client, model: model}, nil
}

// Name identifies the provider and model for the investigation record.
func (m *Model) Name() string { return "gemini/" + m.model }

// Generate sends the conversation and returns the model's next turn.
func (m *Model) Generate(ctx context.Context, req agent.Request) (agent.Response, error) {
	declarations, err := toDeclarations(req.Tools)
	if err != nil {
		return agent.Response{}, err
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: req.System}}},
		Tools:             []*genai.Tool{{FunctionDeclarations: declarations}},
		// Every turn must be a tool call: either gathering evidence or submitting findings. Forcing
		// it removes the class of turns where the model replies in prose and makes no progress.
		ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
			Mode: genai.FunctionCallingConfigModeAny,
		}},
	}

	contents, err := toContents(req.Messages)
	if err != nil {
		return agent.Response{}, err
	}

	resp, err := m.client.Models.GenerateContent(ctx, m.model, contents, config)
	if err != nil {
		return agent.Response{}, err
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return agent.Response{}, errors.New("gemini: response contained no candidate")
	}

	content := resp.Candidates[0].Content
	reply := agent.Message{Role: agent.RoleModel, ProviderState: content}
	for _, part := range content.Parts {
		switch {
		case part.FunctionCall != nil:
			args, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				return agent.Response{}, fmt.Errorf("gemini: encoding call arguments: %w", err)
			}
			reply.ToolCalls = append(reply.ToolCalls, agent.ToolCall{
				ID:        part.FunctionCall.ID,
				Name:      part.FunctionCall.Name,
				Arguments: args,
			})
		case part.Text != "" && !part.Thought:
			reply.Text += part.Text
		}
	}

	return agent.Response{Message: reply, Usage: usageOf(resp)}, nil
}

// usageOf counts thinking tokens as output, since they are generated and billed as output.
func usageOf(resp *genai.GenerateContentResponse) agent.Usage {
	u := resp.UsageMetadata
	if u == nil {
		return agent.Usage{}
	}
	return agent.Usage{
		InputTokens:  int(u.PromptTokenCount),
		OutputTokens: int(u.CandidatesTokenCount) + int(u.ThoughtsTokenCount),
	}
}

func toDeclarations(specs []agent.ToolSpec) ([]*genai.FunctionDeclaration, error) {
	out := make([]*genai.FunctionDeclaration, 0, len(specs))
	for _, s := range specs {
		var schema map[string]any
		if err := json.Unmarshal(s.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("gemini: tool %s has an invalid schema: %w", s.Name, err)
		}
		out = append(out, &genai.FunctionDeclaration{
			Name:                 s.Name,
			Description:          s.Description,
			ParametersJsonSchema: schema,
		})
	}
	return out, nil
}

// toContents converts the conversation. A model turn that carries its original Gemini content is
// replayed as is, because that content holds thought signatures the API requires back unchanged for
// multi turn function calling.
func toContents(messages []agent.Message) ([]*genai.Content, error) {
	out := make([]*genai.Content, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == agent.RoleModel {
			if original, ok := msg.ProviderState.(*genai.Content); ok {
				out = append(out, original)
				continue
			}
			c := &genai.Content{Role: genai.RoleModel}
			if msg.Text != "" {
				c.Parts = append(c.Parts, &genai.Part{Text: msg.Text})
			}
			for _, call := range msg.ToolCalls {
				var args map[string]any
				if err := json.Unmarshal(call.Arguments, &args); err != nil {
					return nil, fmt.Errorf("gemini: decoding call arguments: %w", err)
				}
				c.Parts = append(c.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: call.ID, Name: call.Name, Args: args}})
			}
			out = append(out, c)
			continue
		}

		c := &genai.Content{Role: genai.RoleUser}
		if msg.Text != "" {
			c.Parts = append(c.Parts, &genai.Part{Text: msg.Text})
		}
		for _, r := range msg.ToolResults {
			key := "output"
			if r.IsError {
				key = "error"
			}
			c.Parts = append(c.Parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
				ID:       r.CallID,
				Name:     r.Name,
				Response: map[string]any{key: r.Content},
			}})
		}
		if len(c.Parts) > 0 {
			out = append(out, c)
		}
	}
	return out, nil
}
