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

// Package agent contains the investigation loop. It reasons about a failure and decides what to read
// next, and it has no means of reading anything itself.
//
// Every read goes through a Broker supplied by the caller. This package deliberately holds no
// Kubernetes client, no kubeconfig, and no credential of any kind, and a test enforces that it never
// acquires a dependency that could provide one (ADR-017).
package agent

import (
	"context"
	"encoding/json"
	"errors"
)

// ToolSpec describes one capability the broker offers. It is what a model is shown when deciding
// which tool to call.
type ToolSpec struct {
	Name        string
	Description string

	// InputSchema is a JSON Schema object describing the tool's arguments.
	InputSchema json.RawMessage
}

// Broker is the agent's only window onto the cluster. Implementations are bound to a single
// investigation's scope and enforce it on every call.
type Broker interface {
	// Tools lists what may be called.
	Tools() []ToolSpec

	// Call invokes a tool with JSON arguments and returns its scrubbed textual result. A call that
	// violates scope, schema, or the call ceiling returns an error wrapping ErrDenied.
	Call(ctx context.Context, tool string, arguments json.RawMessage) (string, error)
}

// ErrDenied marks a call the broker refused. It is distinct from a failed read: a denial means the
// request itself was not permitted, and is worth surfacing to a reviewer rather than retrying.
var ErrDenied = errors.New("tool call denied")
