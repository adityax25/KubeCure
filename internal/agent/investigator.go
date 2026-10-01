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
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// extraTurns allows for turns that make no tool call and for rejected submissions, beyond one
	// turn per tool call. It bounds the loop even when a model misbehaves.
	extraTurns = 6

	// StopConcluded is the stop reason recorded when the model submits valid findings.
	StopConcluded = "concluded"

	// concludeThreshold is the fraction of the token or cost budget past which the model is told to
	// conclude rather than continue gathering.
	concludeThreshold = 0.8
)

// Budget bounds one investigation. Each limit is enforced independently; the first reached ends it.
type Budget struct {
	MaxToolCalls    int
	MaxTokens       int
	MaxCostMicroUSD int64
	Timeout         time.Duration
}

// Pricing converts token usage into cost. When both rates are zero, cost is reported as unknown and
// the cost budget is not enforced, since a guessed price would make the limit meaningless.
type Pricing struct {
	InputMicroUSDPerMillion  int64
	OutputMicroUSDPerMillion int64
}

func (p Pricing) known() bool { return p.InputMicroUSDPerMillion > 0 || p.OutputMicroUSDPerMillion > 0 }

// BaselineItem is evidence gathered before the investigation began, offered to the model up front.
type BaselineItem struct {
	// ID is how the model cites it, such as B:Events.
	ID      string
	Kind    string
	Excerpt string
}

// Requirement is a piece of evidence the failure type needs before a conclusion is acceptable.
type Requirement struct {
	Kind string

	// Tool names the tool that can obtain this evidence, given to the model when it is missing.
	Tool string

	// Collected reports that the evidence is already in hand.
	Collected bool

	// Obtainable reports whether the evidence can still be gathered. Evidence that cannot exist in
	// this cluster, such as resource usage with no metrics server, is not obtainable.
	Obtainable bool

	// SatisfiedBy reports whether a tool call gathers this evidence. Logs need it, since the same
	// tool serves current and previous output.
	SatisfiedBy func(tool string, arguments json.RawMessage) bool
}

// Input is everything an investigation starts from.
type Input struct {
	FailureType string
	Namespace   string
	Workload    string
	Container   string

	// Facts are scalar observations already known, such as an exit code or restart count.
	Facts []string

	Baseline       []BaselineItem
	Requirements   []Requirement
	AllowedActions []string

	Budget  Budget
	Pricing Pricing
}

// Findings is the outcome of an investigation.
type Findings struct {
	Hypotheses []Hypothesis

	ToolCalls    int
	Turns        int
	Tokens       int
	CostMicroUSD int64
	CostKnown    bool
	Duration     time.Duration

	// BudgetExhausted reports that a limit ended the investigation before a valid conclusion. The
	// result then carries no hypotheses rather than a guess.
	BudgetExhausted bool

	// Degraded reports that a required evidence item could not be obtained, so confidence was capped.
	Degraded        bool
	MissingRequired []string

	// StopReason explains how the investigation ended.
	StopReason string
}

// Investigator runs the goal directed loop. It reads only through a Broker and reasons only through
// a Model, and holds neither a cluster client nor a credential.
type Investigator struct {
	Model  Model
	Broker Broker
	now    func() time.Time
}

// NewInvestigator builds an investigator over the given model and broker.
func NewInvestigator(model Model, broker Broker) *Investigator {
	return &Investigator{Model: model, Broker: broker, now: time.Now}
}

type loopState struct {
	in           Input
	findings     Findings
	ledger       map[string]string
	allowed      map[string]bool
	started      time.Time
	nextEvidence int
}

// Run investigates until the model submits valid findings or a budget is exhausted. A model or
// transport error is returned as an error, since it says nothing about the failure under
// investigation and is worth retrying.
func (inv *Investigator) Run(ctx context.Context, in Input) (Findings, error) {
	if in.Budget.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, in.Budget.Timeout)
		defer cancel()
	}

	st := &loopState{
		in:      in,
		ledger:  map[string]string{},
		allowed: map[string]bool{},
		started: inv.now(),
		findings: Findings{
			CostKnown: in.Pricing.known(),
		},
		nextEvidence: 1,
	}
	for _, b := range in.Baseline {
		st.ledger[b.ID] = b.Kind
	}
	for _, a := range in.AllowedActions {
		st.allowed[a] = true
	}

	tools := append(append([]ToolSpec(nil), inv.Broker.Tools()...), submitSpec(in.AllowedActions))
	messages := []Message{{Role: RoleUser, Text: initialContext(in)}}
	maxTurns := in.Budget.MaxToolCalls + extraTurns

	defer func() { st.findings.Duration = inv.now().Sub(st.started) }()

	for st.findings.Turns < maxTurns {
		if reason := st.exhausted(ctx); reason != "" {
			return st.stop(reason, true), nil
		}

		st.findings.Turns++
		resp, err := inv.Model.Generate(ctx, Request{System: systemPrompt, Messages: messages, Tools: tools})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return st.stop("wall clock budget exhausted", true), nil
			}
			return st.findings, fmt.Errorf("model %s: %w", inv.Model.Name(), err)
		}
		st.account(resp.Usage)

		reply := resp.Message
		reply.Role = RoleModel
		messages = append(messages, reply)

		if len(reply.ToolCalls) == 0 {
			messages = append(messages, Message{Role: RoleUser, Text: "Continue by calling a tool. " +
				"When the evidence supports a conclusion, call " + SubmitTool + "."})
			continue
		}

		results, done := inv.handleCalls(ctx, st, reply.ToolCalls)
		if done {
			return st.findings, nil
		}

		next := Message{Role: RoleUser, ToolResults: results}
		if st.nearlyExhausted() {
			next.Text = "The investigation budget is nearly exhausted. Call " + SubmitTool +
				" now with the best conclusion the evidence supports."
		}
		messages = append(messages, next)
	}

	return st.stop("turn limit reached without a valid conclusion", true), nil
}

// handleCalls executes one turn's tool calls. It reports done when a valid submission is accepted.
func (inv *Investigator) handleCalls(ctx context.Context, st *loopState, calls []ToolCall) ([]ToolResult, bool) {
	results := make([]ToolResult, 0, len(calls))

	for _, call := range calls {
		if call.Name == SubmitTool {
			hypotheses, problems := st.accept(call.Arguments)
			if problems == nil {
				st.findings.Hypotheses = hypotheses
				st.findings.StopReason = StopConcluded
				return nil, true
			}
			results = append(results, ToolResult{
				CallID: call.ID, Name: call.Name, IsError: true,
				Content: "The submission was rejected and the investigation continues:\n- " +
					strings.Join(problems, "\n- "),
			})
			continue
		}

		if st.findings.ToolCalls >= st.in.Budget.MaxToolCalls {
			results = append(results, ToolResult{
				CallID: call.ID, Name: call.Name, IsError: true,
				Content: "The tool call budget is exhausted. Call " + SubmitTool + " now.",
			})
			continue
		}

		st.findings.ToolCalls++
		content, err := inv.Broker.Call(ctx, call.Name, call.Arguments)
		st.recordRequirement(call, err)

		if err != nil {
			results = append(results, ToolResult{CallID: call.ID, Name: call.Name, IsError: true, Content: err.Error()})
			continue
		}

		id := fmt.Sprintf("E%d", st.nextEvidence)
		st.nextEvidence++
		st.ledger[id] = call.Name
		results = append(results, ToolResult{CallID: call.ID, Name: call.Name, Content: wrapEvidence(id, call.Name, content)})
	}
	return results, false
}

// accept validates a submission against the gathered evidence and the sufficiency gate.
func (st *loopState) accept(arguments json.RawMessage) ([]Hypothesis, []string) {
	hypotheses, problems := validateSubmission(arguments, st.ledger, st.allowed)
	if problems != nil {
		return nil, problems
	}

	var missingObtainable, missingUnobtainable []string
	for _, r := range st.in.Requirements {
		if r.Collected {
			continue
		}
		if r.Obtainable {
			missingObtainable = append(missingObtainable, fmt.Sprintf("%s (call %s)", r.Kind, r.Tool))
		} else {
			missingUnobtainable = append(missingUnobtainable, r.Kind)
		}
	}

	if len(missingObtainable) > 0 && st.findings.ToolCalls < st.in.Budget.MaxToolCalls {
		return nil, []string{"required evidence has not been gathered yet: " + strings.Join(missingObtainable, ", ")}
	}

	missing := append(missingUnobtainable, missingObtainable...)
	if len(missing) > 0 {
		st.findings.Degraded = true
		st.findings.MissingRequired = missing
		capConfidence(hypotheses, degradedConfidenceCap)
	}
	return hypotheses, nil
}

// recordRequirement updates requirement state from a tool call. A successful call satisfies the
// requirement; a failed one marks it unobtainable, so the gate does not demand evidence the cluster
// cannot provide.
func (st *loopState) recordRequirement(call ToolCall, err error) {
	for i := range st.in.Requirements {
		r := &st.in.Requirements[i]
		if r.Collected {
			continue
		}
		matches := r.Tool == call.Name
		if r.SatisfiedBy != nil {
			matches = r.SatisfiedBy(call.Name, call.Arguments)
		}
		if !matches {
			continue
		}
		if err == nil {
			r.Collected = true
		} else if !errors.Is(err, ErrDenied) {
			r.Obtainable = false
		}
	}
}

func (st *loopState) account(u Usage) {
	st.findings.Tokens += u.InputTokens + u.OutputTokens
	if st.findings.CostKnown {
		p := st.in.Pricing
		st.findings.CostMicroUSD += int64(u.InputTokens)*p.InputMicroUSDPerMillion/1_000_000 +
			int64(u.OutputTokens)*p.OutputMicroUSDPerMillion/1_000_000
	}
}

func (st *loopState) exhausted(ctx context.Context) string {
	b := st.in.Budget
	switch {
	case ctx.Err() != nil:
		return "wall clock budget exhausted"
	case b.MaxTokens > 0 && st.findings.Tokens >= b.MaxTokens:
		return "token budget exhausted"
	case st.findings.CostKnown && b.MaxCostMicroUSD > 0 && st.findings.CostMicroUSD >= b.MaxCostMicroUSD:
		return "cost budget exhausted"
	}
	return ""
}

func (st *loopState) nearlyExhausted() bool {
	b := st.in.Budget
	if b.MaxToolCalls > 0 && st.findings.ToolCalls >= b.MaxToolCalls-1 {
		return true
	}
	if b.MaxTokens > 0 && float64(st.findings.Tokens) >= concludeThreshold*float64(b.MaxTokens) {
		return true
	}
	return st.findings.CostKnown && b.MaxCostMicroUSD > 0 &&
		float64(st.findings.CostMicroUSD) >= concludeThreshold*float64(b.MaxCostMicroUSD)
}

func (st *loopState) stop(reason string, exhausted bool) Findings {
	st.findings.StopReason = reason
	st.findings.BudgetExhausted = exhausted
	st.findings.Hypotheses = nil
	return st.findings
}
