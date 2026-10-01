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
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SubmitTool is the tool through which the model concludes. Its schema is the hypothesis structure,
// so a conclusion arrives as validated data rather than prose to be parsed.
const SubmitTool = "submit_findings"

const (
	maxHypotheses    = 5
	rootCauseLimit   = 2048
	actionValueLimit = 253

	// degradedConfidenceCap bounds every confidence when a required evidence item could not be
	// obtained, so a conclusion reached without key evidence cannot present itself as certain.
	degradedConfidenceCap = 70
)

// Action is a remediation the model proposes. Its type is constrained to the remediation enum.
type Action struct {
	Type      string `json:"type"`
	Container string `json:"container,omitempty"`
	Value     string `json:"value,omitempty"`
}

// Hypothesis is one ranked explanation, with the evidence it rests on.
type Hypothesis struct {
	ID                string
	Rank              int
	RootCause         string
	ConfidencePercent int
	EvidenceCitations []string
	ProposedActions   []Action
}

type submission struct {
	Hypotheses []struct {
		RootCause         string   `json:"rootCause"`
		ConfidencePercent int      `json:"confidencePercent"`
		EvidenceCitations []string `json:"evidenceCitations"`
		ProposedActions   []Action `json:"proposedActions"`
	} `json:"hypotheses"`
}

// submitSpec describes the concluding tool, with the action enum filled from the caller.
func submitSpec(allowedActions []string) ToolSpec {
	enum, _ := json.Marshal(allowedActions)
	schema := fmt.Sprintf(`{
		"type":"object",
		"properties":{
			"hypotheses":{
				"type":"array","minItems":1,"maxItems":%d,
				"description":"Candidate explanations, most likely first.",
				"items":{
					"type":"object",
					"properties":{
						"rootCause":{"type":"string","description":"The cause of the failure, stated for an engineer reading a pull request."},
						"confidencePercent":{"type":"integer","minimum":0,"maximum":100},
						"evidenceCitations":{"type":"array","minItems":1,"items":{"type":"string"},
							"description":"Identifiers of the evidence supporting this explanation, such as B:Events or E2. Cite only identifiers that appear in the evidence."},
						"proposedActions":{"type":"array","items":{
							"type":"object",
							"properties":{
								"type":{"type":"string","enum":%s},
								"container":{"type":"string"},
								"value":{"type":"string","description":"The action's parameter, such as 384Mi, an image reference, or a revision number."}
							},
							"required":["type"]}}
					},
					"required":["rootCause","confidencePercent","evidenceCitations"]
				}
			}
		},
		"required":["hypotheses"]
	}`, maxHypotheses, enum)

	return ToolSpec{
		Name: SubmitTool,
		Description: "Conclude the investigation with ranked hypotheses. Call this only once the " +
			"evidence supports a conclusion. Every hypothesis must cite evidence identifiers.",
		InputSchema: json.RawMessage(schema),
	}
}

// validateSubmission parses a submission and checks it against the evidence actually gathered. It
// returns the problems found rather than stopping at the first, so the model can correct everything
// in one turn.
func validateSubmission(arguments json.RawMessage, ledger map[string]string, allowed map[string]bool) ([]Hypothesis, []string) {
	var sub submission
	dec := json.NewDecoder(bytes.NewReader(arguments))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sub); err != nil {
		return nil, []string{fmt.Sprintf("the submission is not valid against the schema: %v", err)}
	}

	var problems []string
	if len(sub.Hypotheses) == 0 {
		problems = append(problems, "submit at least one hypothesis")
	}
	if len(sub.Hypotheses) > maxHypotheses {
		problems = append(problems, fmt.Sprintf("submit at most %d hypotheses", maxHypotheses))
	}

	hypotheses := make([]Hypothesis, 0, len(sub.Hypotheses))
	for i, h := range sub.Hypotheses {
		label := fmt.Sprintf("hypothesis %d", i+1)

		cause := strings.TrimSpace(h.RootCause)
		switch {
		case cause == "":
			problems = append(problems, label+" has no root cause")
		case len(cause) > rootCauseLimit:
			problems = append(problems, fmt.Sprintf("%s root cause exceeds %d characters", label, rootCauseLimit))
		}
		if h.ConfidencePercent < 0 || h.ConfidencePercent > 100 {
			problems = append(problems, label+" confidence must be between 0 and 100")
		}

		if len(h.EvidenceCitations) == 0 {
			problems = append(problems, label+" cites no evidence; every hypothesis must cite what supports it")
		}
		for _, c := range h.EvidenceCitations {
			if _, ok := ledger[c]; !ok {
				problems = append(problems, fmt.Sprintf("%s cites %q, which does not exist; cite only identifiers from the evidence", label, c))
			}
		}

		for _, a := range h.ProposedActions {
			if !allowed[a.Type] {
				problems = append(problems, fmt.Sprintf("%s proposes unknown action type %q", label, a.Type))
			}
			if len(a.Value) > actionValueLimit {
				problems = append(problems, fmt.Sprintf("%s action value exceeds %d characters", label, actionValueLimit))
			}
		}

		hypotheses = append(hypotheses, Hypothesis{
			RootCause:         cause,
			ConfidencePercent: h.ConfidencePercent,
			EvidenceCitations: dedupe(h.EvidenceCitations),
			ProposedActions:   h.ProposedActions,
		})
	}

	if len(problems) > 0 {
		return nil, problems
	}
	return rank(hypotheses), nil
}

// rank orders hypotheses by confidence, keeping the model's order between equals, and assigns
// identifiers and ranks.
func rank(hypotheses []Hypothesis) []Hypothesis {
	sort.SliceStable(hypotheses, func(i, j int) bool {
		return hypotheses[i].ConfidencePercent > hypotheses[j].ConfidencePercent
	})
	for i := range hypotheses {
		hypotheses[i].ID = fmt.Sprintf("H%d", i+1)
		hypotheses[i].Rank = i + 1
	}
	return hypotheses
}

func capConfidence(hypotheses []Hypothesis, ceiling int) {
	for i := range hypotheses {
		if hypotheses[i].ConfidencePercent > ceiling {
			hypotheses[i].ConfidencePercent = ceiling
		}
	}
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
