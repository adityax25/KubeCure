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

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/adityax25/KubeCure/internal/agent"
	"github.com/adityax25/KubeCure/internal/model/gemini"
)

const (
	envAPIKey      = "GEMINI_API_KEY"
	envModel       = "KUBECURE_MODEL"
	envPriceInput  = "KUBECURE_PRICE_INPUT_PER_MILLION_MICROUSD"
	envPriceOutput = "KUBECURE_PRICE_OUTPUT_PER_MILLION_MICROUSD"

	// defaultModel is used when none is configured. Model availability changes over time, so
	// deployments should set the model explicitly rather than rely on this.
	defaultModel = "gemini-2.5-flash"
)

// modelFromEnv builds the investigation model from the environment. It returns a nil model, not an
// error, when no key is set: the operator still runs, and diagnoses wait for the rule engine.
//
// Token prices are read rather than built in, because prices change and a stale constant would make
// the cost budget silently wrong. Without them, cost is reported as unknown.
func modelFromEnv(ctx context.Context) (agent.Model, agent.Pricing, error) {
	key := os.Getenv(envAPIKey)
	if key == "" {
		return nil, agent.Pricing{}, nil
	}

	name := os.Getenv(envModel)
	if name == "" {
		name = defaultModel
	}

	input, err := priceFromEnv(envPriceInput)
	if err != nil {
		return nil, agent.Pricing{}, err
	}
	output, err := priceFromEnv(envPriceOutput)
	if err != nil {
		return nil, agent.Pricing{}, err
	}

	m, err := gemini.New(ctx, key, name)
	if err != nil {
		return nil, agent.Pricing{}, err
	}
	return m, agent.Pricing{InputMicroUSDPerMillion: input, OutputMicroUSDPerMillion: output}, nil
}

func priceFromEnv(name string) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s must be a non negative integer, got %q", name, raw)
	}
	return v, nil
}
