# KubeCure Learning Guide

[← Back to README](../README.md)

> A comprehensive guide for understanding the technologies, patterns, and tradeoffs in building an AI-native Kubernetes self-healing operator.

This guide is designed for someone with a strong CS foundation (MS-level) who is new to the cloud-native ecosystem. Each section provides both **technical depth** and **intuitive explanations**.

---

## Table of Contents

1. [Kubernetes Fundamentals](#1-kubernetes-fundamentals)
2. [Local Kubernetes Options Compared](#2-local-kubernetes-options-compared)
3. [Go for Cloud-Native Development](#3-go-for-cloud-native-development)
4. [Kubernetes Operators & Controllers](#4-kubernetes-operators--controllers)
5. [Operator SDK & Controller Runtime](#5-operator-sdk--controller-runtime)
6. [LLM Integration Patterns](#6-llm-integration-patterns)
7. [GitOps Principles](#7-gitops-principles)
8. [Terraform & Infrastructure as Code](#8-terraform--infrastructure-as-code)
9. [Observability Stack](#9-observability-stack)
10. [Frontend Architecture](#10-frontend-architecture)

---

## 1. Kubernetes Fundamentals

### What is Kubernetes?

**Technical**: Kubernetes (k8s) is a container orchestration platform that manages the lifecycle of containerized applications across a cluster of machines. It provides primitives for deployment, scaling, service discovery, load balancing, and self-healing.

**Intuitive**: Think of Kubernetes as an **operating system for your datacenter**. Just like Linux manages processes on a single machine, Kubernetes manages containers across many machines.

### Core Primitives

| Resource | What It Is | Analogy |
|----------|-----------|---------|
| **Pod** | The smallest deployable unit. One or more containers that share networking/storage. | A single apartment unit (containers are roommates sharing the space) |
| **Deployment** | Declarative updates for Pods. Manages ReplicaSets for rollouts. | A property management company ensuring the right number of apartments exist |
| **Service** | Stable network endpoint for a set of Pods (load balancing + discovery). | A building's address that routes to individual apartments |
| **ConfigMap/Secret** | Configuration data/credentials injected into Pods. | A key lockbox for the building |
| **Event** | A record of something happening in the cluster. | Security camera footage |

### The Control Loop Philosophy

Kubernetes is built on **declarative, eventual consistency**:

```
                    ┌────────────────────────────────┐
                    │                                │
                    ▼                                │
              ┌──────────┐     ┌──────────┐    ┌──────────┐
              │ Desired  │     │  Actual  │    │   Take   │
              │  State   │────▶│  State   │───▶│  Action  │
              │ (YAML)   │     │ (Cluster)│    │          │
              └──────────┘     └──────────┘    └──────────┘
                    │                                │
                    │         Reconcile              │
                    └────────────────────────────────┘
```

**Technical**: Controllers continuously reconcile desired state (stored in etcd via the API server) with actual state (observed from the cluster). This is an **asynchronous, level-triggered** control loop.

**Intuitive**: You tell Kubernetes "I want 3 replicas of my app running." Kubernetes doesn't just do it once—it *continuously* ensures 3 replicas exist, even if one dies at 3am.

### Why This Matters for KubeCure

KubeCure is itself a controller. It watches for Pods in failure states and reconciles by proposing remediation. We're extending the Kubernetes control plane with AI-driven healing.

---

## 2. Local Kubernetes Options Compared

For development, you need a local Kubernetes cluster. Here are your options:

### Comparison Matrix

| Feature | **kind** | **minikube** | **k3s** | **Docker Desktop** |
|---------|----------|--------------|---------|-------------------|
| **Full Name** | Kubernetes in Docker | Mini Kubernetes | Lightweight Kubernetes | Docker's built-in k8s |
| **How It Works** | Runs k8s nodes as Docker containers | Runs k8s in a VM or container | Runs as a single binary (stripped-down k8s) | VM-based, integrated with Docker |
| **Startup Time** | ~30 seconds | ~2-3 minutes | ~30 seconds | ~1-2 minutes |
| **Resource Usage** | Low (~500MB) | Medium (~2GB) | Very Low (~300MB) | Medium (~2GB) |
| **Multi-node** | ✅ Easy | ⚠️ Possible but complex | ✅ Easy | ❌ Single node only |
| **Production Parity** | High (full k8s) | High (full k8s) | Medium (some features removed) | High (full k8s) |
| **Best For** | CI/testing, operator dev | Local dev with add-ons | Edge/IoT, resource-constrained | Docker users who want simplicity |

### Deep Dive: kind (Kubernetes in Docker)

```
┌─────────────────────────────────────────────────────────┐
│                     Docker Host                         │
│  ┌─────────────────┐  ┌─────────────────┐               │
│  │  kind-control-  │  │  kind-worker    │               │
│  │plane (container)│  │  (container)    │               │
│  │  ┌───────────┐  │  │  ┌───────────┐  │               │
│  │  │ kubelet   │  │  │  │ kubelet   │  │               │
│  │  │ API server│  │  │  │ Your Pods │  │               │
│  │  │ etcd      │  │  │  └───────────┘  │               │
│  │  │ scheduler │  │  └─────────────────┘               │
│  │  └───────────┘  │                                    │
│  └─────────────────┘                                    │
└─────────────────────────────────────────────────────────┘
```

**Technical**: kind uses `containerd` inside Docker containers to run Kubernetes nodes. It's literally Kubernetes running inside containers, which are running on your host's Docker daemon.

**Intuitive**: Imagine Russian nesting dolls. Your laptop runs Docker. Docker runs containers that *pretend* to be servers. Those fake servers run Kubernetes. Kubernetes runs your actual application containers.

### Deep Dive: k3s

**What's different about k3s?**

k3s is a stripped-down Kubernetes distribution by Rancher:
- Replaces `etcd` with SQLite (or Postgres/MySQL)
- Removes legacy/alpha features
- Single binary (~60MB)
- Bundles containerd

**Technical Tradeoff**: k3s sacrifices some features (cloud provider integrations, in-tree storage drivers) for simplicity. It's compliant with the Kubernetes API but not a full distribution.

**When to use k3s over kind**:
- You're resource-constrained (old laptop, Raspberry Pi)
- You want to test edge deployment scenarios
- You need a persistent cluster that survives reboots easily

### Our Choice: **kind**

For KubeCure development, I recommend **kind** because:

1. **Full k8s parity** — No surprises when deploying to EKS later
2. **Multi-node support** — Can test node failures
3. **CI-friendly** — Same tool works in GitHub Actions
4. **Fast iteration** — 30-second clusters
5. **Widely adopted** — Used by Kubernetes itself for testing

---

## 3. Go for Cloud-Native Development

### Why Go?

**The Technical Answer:**

| Property | Benefit for Cloud-Native |
|----------|-------------------------|
| **Static binary compilation** | No runtime dependencies. Your operator is one file. |
| **Fast compilation** | Seconds, not minutes. Rapid iteration. |
| **Built-in concurrency** | Goroutines + channels for async event handling |
| **Strong stdlib** | HTTP, JSON, crypto built-in. Few external deps. |
| **Memory safety without GC pauses** | Predictable latency for controllers |

**The Industry Answer:**

Go is the **lingua franca of cloud-native**. These projects are written in Go:
- Kubernetes
- Docker
- containerd
- Prometheus
- etcd
- Terraform
- Istio
- Every major Kubernetes operator framework

If you're building for the cloud-native ecosystem, Go is the path of least resistance.

### Go for the Kubernetes Ecosystem

The Kubernetes Go client libraries (`client-go`, `controller-runtime`) are first-class citizens. Python/JS clients exist but are wrappers with less documentation and community support.

```go
// Example: Watching Pods in Go
informer := cache.NewSharedInformer(
    &cache.ListWatch{
        ListFunc:  func(opts metav1.ListOptions) (runtime.Object, error) { ... },
        WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) { ... },
    },
    &corev1.Pod{},
    0,
)
```

### Go Module Structure

```
kubecure/
├── go.mod              # Dependency manifest (like package.json)
├── go.sum              # Dependency lock file
├── cmd/                # Entrypoints (main packages)
│   └── operator/
│       └── main.go
├── internal/           # Private code (can't be imported externally)
│   ├── controller/
│   ├── detector/
│   └── ai/
└── pkg/                # Public code (can be imported)
    └── apis/
```

**Why `internal/`?** — Go enforces that packages under `internal/` cannot be imported by code outside the module. This is a **compile-time guarantee** of encapsulation.

---

## 4. Kubernetes Operators & Controllers

### What is a Controller?

**Technical Definition**: A control loop that watches the state of your cluster via the Kubernetes API and makes changes to move the current state toward the desired state.

**Intuitive**: A thermostat. You set the desired temperature (declarative state). The thermostat continuously checks the actual temperature and turns heating/cooling on/off to reconcile.

### What is an Operator?

**Technical Definition**: An Operator is a Controller that encodes domain-specific operational knowledge. It typically extends Kubernetes with Custom Resource Definitions (CRDs) and manages complex stateful applications.

**Intuitive**: A Controller is the thermostat logic. An Operator is a smart home system that knows about heating, cooling, humidity, AND knows that when you leave for vacation, it should lower the temp.

### The Operator Pattern

```
┌──────────────────────────────────────────────────────────────┐
│                    Kubernetes API Server                     │
└──────────────────────────────────────────────────────────────┘
        │                                        ▲
        │ Watch (Informer)                       │ Update
        ▼                                        │
┌──────────────────────────────────────────────────────────────┐
│                     Your Operator                            │
│  ┌─────────────┐    ┌──────────────┐    ┌────────────────┐   │
│  │   Informer  │───▶│  Work Queue  │───▶│  Reconciler    │   │
│  │   (cache)   │    │              │    │  (your logic)  │   │
│  └─────────────┘    └──────────────┘    └────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

**Key Components:**

1. **Informer**: Maintains a local cache of resources, watches for changes
2. **Work Queue**: Buffers and deduplicates events (rate limiting, retries)
3. **Reconciler**: Your business logic. Given a key (namespace/name), reconcile state.

### Reconciliation: Level-Triggered vs Edge-Triggered

**Edge-triggered**: "React when something changes" (event-driven)
**Level-triggered**: "Continuously ensure current state matches desired state"

Kubernetes uses **level-triggered** reconciliation:

```go
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. Fetch the current state
    pod := &corev1.Pod{}
    if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    
    // 2. Determine what needs to happen
    if pod.Status.Phase == corev1.PodFailed {
        // 3. Take action
        return r.handleFailure(ctx, pod)
    }
    
    return ctrl.Result{}, nil
}
```

**Why Level-Triggered?** — It's idempotent and resilient. If your controller crashes mid-reconciliation, it just picks up where it left off. No missed events.

---

## 5. Operator SDK & Controller Runtime

### What is `controller-runtime`?

A Go library from the Kubernetes SIGs (Special Interest Groups) that provides:
- Higher-level abstractions over `client-go`
- Manager pattern for running multiple controllers
- Caching client for efficient API access
- Webhook support
- Built-in health checks and metrics

**Intuitive**: `client-go` is raw Kubernetes API access (like using `fetch()` for HTTP). `controller-runtime` is a framework built on top (like using Express or Gin).

### What is Operator SDK?

A toolkit by Red Hat that wraps `controller-runtime` with:
- Project scaffolding (`operator-sdk init`)
- CRD code generation
- Testing utilities
- Helm and Ansible operator support
- OLM (Operator Lifecycle Manager) integration

```bash
# Scaffold a new operator
operator-sdk init --domain kubecure.io --repo github.com/adityax25/kubecure

# Create a new API (CRD)
operator-sdk create api --group healing --version v1alpha1 --kind HealingPolicy
```

### The Tradeoff: Operator SDK vs Raw controller-runtime

| Aspect | Operator SDK | Raw controller-runtime |
|--------|--------------|------------------------|
| **Learning curve** | Lower (guided scaffolding) | Steeper (more manual setup) |
| **Flexibility** | Opinionated structure | Full control |
| **Boilerplate** | Less | More |
| **Updates** | Lag behind controller-runtime | Latest features immediately |
| **Use when** | Starting out, standard patterns | Custom layouts, minimal deps |

**For KubeCure**: We'll use **Operator SDK** for scaffolding, but understand the underlying `controller-runtime` patterns so you're not boxed in.

---

## 6. LLM Integration Patterns

### Why Gemini?

| Factor | Gemini | GPT-4 | Claude |
|--------|--------|-------|--------|
| **Context Window** | 1M+ tokens | 128K tokens | 200K tokens |
| **Cost** | Lower | Higher | Medium |
| **Code Understanding** | Excellent | Excellent | Excellent |
| **Structured Output** | Native JSON mode | Function calling | Tool use |
| **Google Cloud Integration** | Native | Via OpenAI API | Via Anthropic API |

**For KubeCure**: Gemini's massive context window means we can fit entire YAML manifests, logs, and events in a single request.

### Prompt Engineering for Diagnostics

**Bad Prompt:**
```
A pod is failing. Here are logs. Fix it.
```

**Good Prompt:**
```
You are a Kubernetes SRE analyzing a pod failure.

## Context
- Pod: my-app-7b4d5f6c-xz9p2
- Namespace: production
- Phase: CrashLoopBackOff
- Restart Count: 5

## Container Logs (last 50 lines)
<logs>

## Pod Manifest
<yaml>

## Recent Events
<events>

## Task
1. Identify the root cause
2. Provide a confidence score (0-100)
3. If confidence > 80, provide an exact YAML patch to fix the issue
4. Return response as JSON: {"root_cause": "...", "confidence": N, "fix": {...}}
```

### Interface-Driven Design

To swap between LLM providers, use an interface:

```go
type DiagnosticEngine interface {
    Diagnose(ctx context.Context, input DiagnosticInput) (*DiagnosticResult, error)
}

type GeminiEngine struct {
    client *genai.Client
}

type OpenAIEngine struct {
    client *openai.Client
}
```

**Why?** — Tomorrow you might want to use a self-hosted LLM, or the user might prefer GPT-4. Interfaces make this trivial.

---

## 7. GitOps Principles

### What is GitOps?

**Technical**: An operational framework where Git repositories are the single source of truth for declarative infrastructure and applications. Changes are made via Pull Requests, and automated systems sync the Git state to the target environment.

**Intuitive**: Instead of `kubectl apply` manually, you commit YAML to Git. A robot watches Git and applies changes for you. Every change is reviewed, versioned, and auditable.

### The GitOps Flow

```
Developer       GitHub          ArgoCD/Flux       Kubernetes
    │              │                 │                │
    │──(1) Push───▶│                 │                │
    │              │◀──(2) Detect────│                │
    │              │     change      │                │
    │              │                 │──(3) Sync─────▶│
    │              │                 │                │
    │◀───────────────── (4) Deployed ─────────────────┘
```

### KubeCure's Role in GitOps

KubeCure is **not** a GitOps operator (like ArgoCD). Instead, it:

1. Detects failures in the cluster
2. Proposes fixes by creating **Pull Requests** in the source repo
3. Lets humans (or automated pipelines) merge and deploy

This maintains the GitOps principle: **Git is the source of truth**, not the cluster.

```
KubeCure Action          │  GitOps Compliance
─────────────────────────┼─────────────────────────────
Create PR with fix       │  ✅ Change goes through Git
Auto-apply to cluster    │  ❌ Bypasses Git (anti-pattern)
Create Issue for review  │  ✅ Human in the loop
```

---

## 8. Terraform & Infrastructure as Code

### What is Terraform?

**Technical**: A declarative infrastructure-as-code tool that provisions and manages cloud resources across multiple providers (AWS, GCP, Azure, etc.) using a configuration language (HCL).

**Intuitive**: YAML for Kubernetes describes your app. Terraform describes the infrastructure your app runs on—VPCs, subnets, Kubernetes clusters, databases, etc.

### Why Terraform for EKS?

To run a production-grade EKS cluster, you need:
- VPC with public/private subnets
- Internet Gateway, NAT Gateway
- Security Groups
- IAM roles for nodes and controllers
- The EKS cluster itself
- Managed node groups

Doing this manually via the AWS console is:
1. Error-prone
2. Not reproducible
3. Undocumented

Terraform makes it:
1. Version-controlled
2. Reviewable via PR
3. Idempotent (run 100 times, same result)

### Terraform vs Other IaC Tools

| Tool | Language | Cloud Support | State Management |
|------|----------|---------------|------------------|
| **Terraform** | HCL (declarative) | Multi-cloud | Remote state (S3, etc.) |
| **Pulumi** | TypeScript/Python/Go | Multi-cloud | Pulumi Cloud |
| **CloudFormation** | YAML/JSON | AWS only | AWS-managed |
| **CDK** | TypeScript/Python | AWS primarily | Synthesizes to CFN |

**For KubeCure**: Terraform is the industry standard for multi-cloud IaC. Even if you're AWS-only now, the skills transfer.

### Hybrid Approach: Local + Cloud

| Phase | Environment | Tool | Purpose |
|-------|-------------|------|---------|
| Development | Local | `kind` | Fast iteration, no cost |
| Staging/Demo | AWS | Terraform + EKS | Real cloud behavior |
| CI Tests | GitHub Actions | `kind` | Automated testing |

---

## 9. Observability Stack

### The Three Pillars

| Pillar | What It Captures | Tool |
|--------|------------------|------|
| **Metrics** | Numeric time-series data (counters, gauges, histograms) | Prometheus |
| **Logs** | Discrete events with context | stdout → Loki or CloudWatch |
| **Traces** | Request flow across services | Jaeger, OpenTelemetry |

### Prometheus + Grafana for KubeCure

```
┌────────────┐        ┌────────────┐        ┌────────────┐
│  KubeCure  │───────▶│ Prometheus │◀──────▶│  Grafana   │
│ (exposes   │ scrape │ (stores    │ query  │(visualize) │
│  /metrics) │        │  metrics)  │        │            │
└────────────┘        └────────────┘        └────────────┘
```

**Metrics we'll expose:**

```go
var (
    failuresDetected = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "kubecure_failures_detected_total",
            Help: "Total number of pod failures detected",
        },
        []string{"namespace", "failure_type"},
    )
    
    remediationConfidence = prometheus.NewHistogram(
        prometheus.HistogramOpts{
            Name:    "kubecure_remediation_confidence",
            Help:    "Confidence scores from LLM diagnoses",
            Buckets: []float64{0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
        },
    )
)
```

### Structured Logging

Instead of:
```
log.Println("pod failed")
```

Use structured logging:
```go
logger.Info("pod failure detected",
    "pod", pod.Name,
    "namespace", pod.Namespace,
    "phase", pod.Status.Phase,
    "restartCount", pod.Status.ContainerStatuses[0].RestartCount,
)
```

**Why?** — Structured logs are queryable. You can search for `namespace="production" AND restartCount>5`.

---

## 10. Frontend Architecture

### Tech Stack

| Technology | Purpose |
|------------|---------|
| **React** | Component-based UI |
| **TypeScript** | Type safety |
| **Framer Motion** | Animations |
| **Tailwind CSS** | Utility-first styling |

### What the Dashboard Will Show

1. **Real-time failure feed** — Pods failing, with status
2. **Diagnosis results** — Root cause, confidence score
3. **Remediation actions** — PR links, Issue links
4. **Cluster health overview** — Metrics from Prometheus

### Communication with Backend

Options:
1. **REST API** — Simple, polling-based
2. **WebSockets** — Real-time streaming
3. **Server-Sent Events (SSE)** — One-way streaming from server

**For real-time failure feed, WebSockets or SSE preferred.**

---

## Quick Reference: Project Phases

| Phase | Focus | What You'll Build |
|-------|-------|-------------------|
| 1 | Setup | Install Go, Docker, kind, kubectl |
| 2 | Go Basics | Hello world, modules, project structure |
| 3 | Operators | Scaffold with operator-sdk |
| 4 | Watch Layer | Pod informers, failure detection |
| 5 | Terraform (parallel) | EKS cluster definition |
| 6 | Aggregation | Log/event/manifest collection |
| 7 | AI Integration | Gemini API, prompt engineering |
| 8 | GitOps | GitHub API for PRs/Issues |
| 9 | Observability | Prometheus metrics, structured logs |
| 10 | Frontend | React dashboard |
| 11 | Integration | End-to-end testing on EKS |
