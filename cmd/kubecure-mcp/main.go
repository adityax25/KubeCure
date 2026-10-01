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

// Command kubecure-mcp serves one diagnosis's investigation tools over the Model Context Protocol on
// standard input and output.
//
// It lets an operator point any MCP client at a live failure and investigate it with exactly the
// tools, scope, and guardrails the in cluster agent uses. Standard output carries the protocol, so
// all diagnostics are written to standard error.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
	"github.com/adityax25/KubeCure/internal/tools"
)

// version identifies this build to MCP clients.
const version = "0.1.0"

func main() {
	logger := log.New(os.Stderr, "kubecure-mcp: ", 0)

	target := flag.String("diagnosis", "", "the diagnosis to investigate, as namespace/name")
	maxCalls := flag.Int("max-calls", 25, "ceiling on tool calls for this session")
	flag.Parse()

	namespace, name, ok := strings.Cut(*target, "/")
	if !ok || namespace == "" || name == "" {
		logger.Fatal("--diagnosis is required, in the form namespace/name")
	}

	if err := run(namespace, name, *maxCalls, logger); err != nil {
		logger.Fatal(err)
	}
}

func run(namespace, name string, maxCalls int, logger *log.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("loading cluster configuration: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := healingv1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("building client: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	diagnosis := &healingv1alpha1.Diagnosis{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, diagnosis); err != nil {
		return fmt.Errorf("loading diagnosis %s/%s: %w", namespace, name, err)
	}

	scope := tools.ScopeForDiagnosis(diagnosis, maxCalls)
	broker := tools.NewRegistry(c, clientset).BindTo(scope)

	logger.Printf("serving %d tools for %s/%s, scoped to %s/%s in namespace %s",
		len(broker.Tools()), namespace, name, scope.OwnerKind, scope.OwnerName, scope.Namespace)

	return tools.NewMCPServer(broker, version).Run(ctx, &mcp.StdioTransport{})
}
