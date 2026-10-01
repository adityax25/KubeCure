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

// Package tools is the read path between the investigation agent and the cluster. It holds the
// clients the agent is never given, binds every investigation to a scope, and scrubs and records
// every result (ADR-017).
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
	"github.com/adityax25/KubeCure/internal/agent"
	"github.com/adityax25/KubeCure/internal/evidence"
)

const (
	// resultLimit caps what a single call returns, matching the ceiling for stored evidence.
	resultLimit = 8192
	// defaultMaxCalls is the broker's own ceiling when the scope does not set one. It sits above any
	// sensible agent budget and exists as a second line of defence, not as the primary limit.
	defaultMaxCalls = 25
	// auditFieldLimit matches the schema's limit on recorded arguments and summaries.
	auditFieldLimit = 512
)

// handler executes one tool against a bound broker. The broker is passed in, rather than captured,
// so that every handler resolves pods and namespaces through the same scope checks.
type handler func(ctx context.Context, b *Broker, arguments json.RawMessage) (string, error)

type tool struct {
	spec agent.ToolSpec
	run  handler
}

// Registry is the single definition of every tool. Both the in process broker and the MCP server
// are built from it, so the agent and any external client see an identical catalogue.
type Registry struct {
	client    client.Client
	collector *evidence.Collector
	tools     []tool
	byName    map[string]tool
}

// NewRegistry builds the catalogue over the given clients. These clients carry the operator's
// credentials, and they never leave this package.
func NewRegistry(c client.Client, clientset kubernetes.Interface) *Registry {
	r := &Registry{
		client:    c,
		collector: &evidence.Collector{Client: c, Clientset: clientset},
		byName:    map[string]tool{},
	}
	for _, t := range catalog() {
		r.tools = append(r.tools, t)
		r.byName[t.spec.Name] = t
	}
	return r
}

// Specs lists every tool in the catalogue.
func (r *Registry) Specs() []agent.ToolSpec {
	specs := make([]agent.ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		specs = append(specs, t.spec)
	}
	return specs
}

// BindTo returns a broker confined to one investigation. Nothing outside the scope is reachable
// through it: the namespace is fixed here and is never an argument a caller can supply.
func (r *Registry) BindTo(scope Scope) *Broker {
	if scope.MaxCalls <= 0 {
		scope.MaxCalls = defaultMaxCalls
	}
	return &Broker{registry: r, scope: scope, now: time.Now}
}

// Broker is a registry bound to a single scope. It satisfies agent.Broker.
type Broker struct {
	registry *Registry
	scope    Scope
	now      func() time.Time

	mu    sync.Mutex
	calls []healingv1alpha1.ToolCall
}

var _ agent.Broker = (*Broker)(nil)

// Tools lists the catalogue available to this investigation.
func (b *Broker) Tools() []agent.ToolSpec { return b.registry.Specs() }

// Scope reports what this broker is confined to.
func (b *Broker) Scope() Scope { return b.scope }

// Calls returns the audit record of every call made through this broker, including denied ones.
func (b *Broker) Calls() []healingv1alpha1.ToolCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]healingv1alpha1.ToolCall(nil), b.calls...)
}

// Call runs one tool. The order of checks is fixed: ceiling, then existence, then the tool's own
// argument and scope validation, then the read itself, then scrubbing. A result is never returned
// unscrubbed, and every outcome is recorded.
func (b *Broker) Call(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	started := b.now()
	record := healingv1alpha1.ToolCall{
		Tool:      name,
		Arguments: clip(compactJSON(arguments), auditFieldLimit),
	}

	result, err := b.call(ctx, name, arguments)

	record.DurationMillis = b.now().Sub(started).Milliseconds()
	if err != nil {
		record.Error = clip(err.Error(), auditFieldLimit)
	} else {
		record.Summary = clip(summarise(result), auditFieldLimit)
	}

	b.mu.Lock()
	b.calls = append(b.calls, record)
	b.mu.Unlock()

	return result, err
}

func (b *Broker) call(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	b.mu.Lock()
	made := len(b.calls)
	b.mu.Unlock()
	if made >= b.scope.MaxCalls {
		return "", denied("call ceiling of %d reached for this investigation", b.scope.MaxCalls)
	}

	t, ok := b.registry.byName[name]
	if !ok {
		return "", denied("unknown tool %q", name)
	}

	raw, err := t.run(ctx, b, arguments)
	if err != nil {
		return "", err
	}

	scrubbed, _ := evidence.Redact(raw)
	return evidence.Truncate(scrubbed, resultLimit), nil
}

// denied builds an error that callers can distinguish from a failed read with errors.Is.
func denied(format string, args ...any) error {
	return fmt.Errorf("%w: %s", agent.ErrDenied, fmt.Sprintf(format, args...))
}

// IsDenied reports whether an error is a refusal rather than a failed read.
func IsDenied(err error) bool { return errors.Is(err, agent.ErrDenied) }

// decodeArgs parses tool arguments strictly. Unknown fields are rejected rather than ignored, so a
// caller cannot smuggle in a parameter, such as a namespace, that the tool does not declare.
func decodeArgs[T any](arguments json.RawMessage, into *T) error {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		trimmed = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return denied("invalid arguments: %v", err)
	}
	if dec.More() {
		return denied("invalid arguments: trailing content")
	}
	return nil
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// summarise reduces a result to a short description for the audit record. The full result is
// already available to the agent; the record only needs to show what kind of answer came back.
func summarise(result string) string {
	firstLine := strings.TrimSpace(strings.SplitN(strings.TrimSpace(result), "\n", 2)[0])
	return fmt.Sprintf("%d bytes: %s", len(result), clip(firstLine, 160))
}

func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}
