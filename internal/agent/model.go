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
	"context"
	"encoding/json"
)

// Role identifies who produced a message in the conversation.
type Role string

const (
	// RoleUser carries the investigation's context and the results of tool calls.
	RoleUser Role = "user"
	// RoleModel carries the model's reasoning and its tool calls.
	RoleModel Role = "model"
)

// ToolCall is a model's request to invoke a tool.
type ToolCall struct {
	// ID correlates a call with its result. Providers that do not issue identifiers leave it empty.
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// Message is one turn of the conversation.
type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult

	// ProviderState is opaque data a model adapter attaches to its own turns and expects back
	// unchanged, such as Gemini thought signatures. The loop never inspects or modifies it; it only
	// replays it, because rebuilding a turn from the fields above would silently discard it.
	ProviderState any
}

// Usage reports tokens consumed by one model call.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Request is one call to a model.
type Request struct {
	System   string
	Messages []Message
	Tools    []ToolSpec
}

// Response is a model's reply to a Request.
type Response struct {
	Message Message
	Usage   Usage
}

// Model generates the next turn of an investigation. Implementations hold their own credentials;
// this package never sees them.
type Model interface {
	// Name identifies the provider and model, for the investigation record.
	Name() string
	Generate(ctx context.Context, req Request) (Response, error)
}
