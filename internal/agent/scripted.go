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
	"fmt"
	"sync"
)

// Turn produces one scripted model reply from the request it receives.
type Turn func(req Request) (Response, error)

// ScriptedModel replays predetermined turns. It makes the loop testable offline and
// deterministically, without credentials or spend, and records every request it was sent so a test
// can assert on what the loop actually transmitted.
type ScriptedModel struct {
	Turns []Turn

	mu       sync.Mutex
	Requests []Request
}

var _ Model = (*ScriptedModel)(nil)

// Name identifies the scripted model in investigation records.
func (m *ScriptedModel) Name() string { return "scripted" }

// Generate returns the next scripted turn, failing once the script runs out.
func (m *ScriptedModel) Generate(_ context.Context, req Request) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	i := len(m.Requests)
	m.Requests = append(m.Requests, req)
	if i >= len(m.Turns) {
		return Response{}, fmt.Errorf("script exhausted after %d turns", len(m.Turns))
	}
	return m.Turns[i](req)
}

// Calls builds a turn that makes the given tool calls.
func Calls(usage Usage, calls ...ToolCall) Turn {
	return func(Request) (Response, error) {
		return Response{Message: Message{Role: RoleModel, ToolCalls: calls}, Usage: usage}, nil
	}
}
