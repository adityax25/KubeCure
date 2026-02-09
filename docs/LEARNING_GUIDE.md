# KubeCure Learning Guide

[Back to README](../README.md)

> A comprehensive guide for understanding the technologies, patterns, and tradeoffs in building an AI-native Kubernetes self-healing operator.

This guide is designed for someone with a strong CS foundation who is new to the cloud-native ecosystem. Each section provides both **technical depth** and **intuitive explanations**.

---

## Table of Contents

1. [Kubernetes Fundamentals](#1-kubernetes-fundamentals)
2. [Kubernetes Architecture Deep Dive](#2-kubernetes-architecture-deep-dive)
3. [Local Kubernetes Options Compared](#3-local-kubernetes-options-compared)
4. [Go for Cloud-Native Development](#4-go-for-cloud-native-development)
5. [Kubernetes Operators and Controllers](#5-kubernetes-operators-and-controllers)
6. [Operator SDK and Controller Runtime](#6-operator-sdk-and-controller-runtime)
7. [Failure Detection Implementation](#7-failure-detection-implementation)
8. [LLM Integration Patterns](#8-llm-integration-patterns)
9. [GitOps Principles](#9-gitops-principles)
10. [Terraform and Infrastructure as Code](#10-terraform-and-infrastructure-as-code)
11. [Observability Stack](#11-observability-stack)
12. [Frontend Architecture](#12-frontend-architecture)

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
                    +---------------------------------+
                    |                                 |
                    v                                 |
              +----------+     +----------+    +----------+
              | Desired  |     |  Actual  |    |   Take   |
              |  State   |---->|  State   |--->|  Action  |
              |  (YAML)  |     | (Cluster)|    |          |
              +----------+     +----------+    +----------+
                    |                                 |
                    |         Reconcile               |
                    +---------------------------------+
```

**Technical**: Controllers continuously reconcile desired state (stored in etcd via the API server) with actual state (observed from the cluster). This is an **asynchronous, level-triggered** control loop.

**Intuitive**: You tell Kubernetes "I want 3 replicas of my app running." Kubernetes doesn't just do it once—it *continuously* ensures 3 replicas exist, even if one dies at 3am.

### Why This Matters for KubeCure

KubeCure is itself a controller. It watches for Pods in failure states and reconciles by proposing remediation. We're extending the Kubernetes control plane with AI-driven healing.

---

## 2. Kubernetes Architecture Deep Dive

### Cluster Structure

A Kubernetes cluster consists of:

```
+----------------------------------------------------------------------+
|                        KUBERNETES CLUSTER                            |
|                                                                      |
|  +----------------------------------------------------------------+  |
|  |                     CONTROL PLANE NODE                         |  |
|  |                                                                |  |
|  |   +----------------+   +----------------+   +----------------+ |  |
|  |   |   API SERVER   |   |   SCHEDULER    |   |   CONTROLLER   | |  |
|  |   |                |   |                |   |    MANAGER     | |  |
|  |   |  REST API for  |   | Decides which  |   | Built-in       | |  |
|  |   |  all k8s ops   |   | node runs pod  |   | controllers    | |  |
|  |   +-------+--------+   +----------------+   +----------------+ |  |
|  |           |                                                    |  |
|  |           | reads/writes                                       |  |
|  |           v                                                    |  |
|  |   +----------------+                                           |  |
|  |   |     etcd       |                                           |  |
|  |   |   (database)   |                                           |  |
|  |   +----------------+                                           |  |
|  +----------------------------------------------------------------+  |
|                                                                      |
|  +------------------------+  +------------------------+              |
|  |      WORKER NODE 1     |  |      WORKER NODE 2     |              |
|  |  +-------+  +-------+  |  |  +-------+  +-------+  |              |
|  |  | Pod A |  | Pod B |  |  |  | Pod C |  | Pod D |  |              |
|  |  +-------+  +-------+  |  |  +-------+  +-------+  |              |
|  |       kubelet          |  |       kubelet          |              |
|  +------------------------+  +------------------------+              |
+----------------------------------------------------------------------+
```

### Control Plane Components

| Component | What It Does |
|-----------|--------------|
| **API Server** | Central REST API. All operations go through here. The ONLY component that talks to etcd. |
| **etcd** | Distributed key-value store. Stores ALL cluster state (pods, services, secrets). |
| **Scheduler** | Watches for new pods without assigned nodes, selects nodes for them. |
| **Controller Manager** | Runs built-in controllers (Deployment, ReplicaSet, Node, etc.). |

### What is etcd?

**Full form**: Pronounced "et-see-dee" — comes from the Unix `/etc` directory + "d" for distributed.

| Aspect | Description |
|--------|-------------|
| **What** | Distributed key-value database |
| **Purpose** | Stores ALL cluster state (pods, services, secrets, configs) |
| **Who uses it** | Only the API Server (not you, not KubeCure directly) |
| **How it works** | Like a giant JSON store: `key: "/pods/default/nginx" -> {pod yaml}` |

**Analogy**: etcd is the **filing cabinet** of Kubernetes. The API Server is the **receptionist** who manages access to it. You never open the filing cabinet yourself—you ask the receptionist.

### How KubeCure Communicates

KubeCure **never talks to Pods directly**. Everything goes through the API Server:

```
+-----------------------------------------------------------------------+
|                                                                       |
|    KubeCure                                                           |
|        |                                                              |
|        | HTTP REST calls (via controller-runtime client)              |
|        v                                                              |
|   +-----------+      +-----------+                                    |
|   | API SERVER|<---->|    etcd   |                                    |
|   |           |      |  (storage)|                                    |
|   +-----+-----+      +-----------+                                    |
|         |                                                             |
|         | (API Server queries nodes)                                  |
|         v                                                             |
|   +--------------+--------------+--------------+                      |
|   |    Node 1    |    Node 2    |    Node 3    |                      |
|   |  +-------+   |  +-------+   |  +-------+   |                      |
|   |  | Pod A |   |  | Pod C |   |  | Pod E |   |                      |
|   |  | Pod B |   |  | Pod D |   |  | Pod F |   |                      |
|   |  +-------+   |  +-------+   |  +-------+   |                      |
|   +--------------+--------------+--------------+                      |
|                                                                       |
+-----------------------------------------------------------------------+
```

### Cluster-Wide Watching

When a controller watches resources, it watches the **entire cluster**, not a single node. The API Server has a global view of all pods across all nodes.

---

## 3. Local Kubernetes Options Compared

For development, you need a local Kubernetes cluster. Here are your options:

### Comparison Matrix

| Feature | **kind** | **minikube** | **k3s** | **Docker Desktop** |
|---------|----------|--------------|---------|-------------------|
| **Full Name** | Kubernetes in Docker | Mini Kubernetes | Lightweight Kubernetes | Docker's built-in k8s |
| **How It Works** | Runs k8s nodes as Docker containers | Runs k8s in a VM or container | Runs as a single binary (stripped-down k8s) | VM-based, integrated with Docker |
| **Startup Time** | ~30 seconds | ~2-3 minutes | ~30 seconds | ~1-2 minutes |
| **Resource Usage** | Low (~500MB) | Medium (~2GB) | Very Low (~300MB) | Medium (~2GB) |
| **Multi-node** | Easy | Possible but complex | Easy | Single node only |
| **Production Parity** | High (full k8s) | High (full k8s) | Medium (some features removed) | High (full k8s) |
| **Best For** | CI/testing, operator dev | Local dev with add-ons | Edge/IoT, resource-constrained | Docker users who want simplicity |

### Deep Dive: kind (Kubernetes in Docker)

```
+-----------------------------------------------------------+
|                     Docker Host                           |
|  +---------------------+  +---------------------+         |
|  |  kind-control-      |  |  kind-worker        |         |
|  |  plane (container)  |  |  (container)        |         |
|  |  +--------------+   |  |  +--------------+   |         |
|  |  | kubelet      |   |  |  | kubelet      |   |         |
|  |  | API server   |   |  |  | Your Pods    |   |         |
|  |  | etcd         |   |  |  +--------------+   |         |
|  |  | scheduler    |   |  +---------------------+         |
|  |  +--------------+   |                                  |
|  +---------------------+                                  |
+-----------------------------------------------------------+
```

**Technical**: kind uses `containerd` inside Docker containers to run Kubernetes nodes. It's literally Kubernetes running inside containers, which are running on your host's Docker daemon.

**Intuitive**: Imagine Russian nesting dolls. Your laptop runs Docker. Docker runs containers that *pretend* to be servers. Those fake servers run Kubernetes. Kubernetes runs your actual application containers.

### kind Naming Conventions

When you create a cluster with `kind create cluster --name kubecure-dev`:

| Name | What It Is |
|------|-----------|
| `kubecure-dev` | **Cluster name** (what you passed to `--name`) |
| `kubecure-dev-control-plane` | **Docker container name** (kind auto-generated: `{cluster}-control-plane`) |
| `kind-kubecure-dev` | **kubectl context name** (kind auto-generated: `kind-{cluster}`) |

**Single-node cluster**: In a single-node kind cluster, the control plane container IS the entire cluster. It runs both control plane components AND your workloads.

### Our Choice: kind

For KubeCure development, we use **kind** because:

1. **Full k8s parity** — No surprises when deploying to EKS later
2. **Multi-node support** — Can test node failures
3. **CI-friendly** — Same tool works in GitHub Actions
4. **Fast iteration** — 30-second clusters
5. **Widely adopted** — Used by Kubernetes itself for testing

---

## 4. Go for Cloud-Native Development

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

### Go Module Structure

```
kubecure/
+-- go.mod              # Dependency manifest (like package.json)
+-- go.sum              # Dependency lock file
+-- cmd/                # Entrypoints (main packages)
|   +-- main.go
+-- internal/           # Private code (can't be imported externally)
|   +-- controller/
|   +-- detector/
|   +-- ai/
+-- pkg/                # Public code (can be imported)
    +-- apis/
```

**Why `internal/`?** — Go enforces that packages under `internal/` cannot be imported by code outside the module. This is a **compile-time guarantee** of encapsulation.

---

## 5. Kubernetes Operators and Controllers

### What is a Controller?

**Technical Definition**: A control loop that watches the state of your cluster via the Kubernetes API and makes changes to move the current state toward the desired state.

**Intuitive**: A thermostat. You set the desired temperature (declarative state). The thermostat continuously checks the actual temperature and turns heating/cooling on/off to reconcile.

### What is an Operator?

**Technical Definition**: An Operator is a Controller that encodes domain-specific operational knowledge. It typically extends Kubernetes with Custom Resource Definitions (CRDs) and manages complex stateful applications.

**Intuitive**: A Controller is the thermostat logic. An Operator is a smart home system that knows about heating, cooling, humidity, AND knows that when you leave for vacation, it should lower the temp.

### Manager vs Controller vs Operator

Think of them as a hierarchy:

```
+-----------------------------------------------------------+
|                      OPERATOR                             |
|  (The whole project - KubeCure)                           |
|                                                           |
|  +-----------------------------------------------------+  |
|  |                    MANAGER                          |  |
|  |  (The runtime that orchestrates everything)         |  |
|  |                                                     |  |
|  |  +--------------+  +---------------+                |  |
|  |  | Controller 1 |  | Controller 2  |  ...           |  |
|  |  | (Pod watcher)|  |(Event watcher)|                |  |
|  |  +--------------+  +---------------+                |  |
|  |                                                     |  |
|  +-----------------------------------------------------+  |
+-----------------------------------------------------------+
```

| Term | What It Is | Scope |
|------|-----------|-------|
| **Controller** | One reconciliation loop for one resource type | Single responsibility |
| **Manager** | Runtime that hosts controllers + shared infra | Process-level orchestrator |
| **Operator** | Complete system (binary + manifests + CRDs) | The deployable product |

### The Manager

The Manager provides shared infrastructure:
- Kubernetes client (for API calls)
- Shared cache (so controllers don't each query the API)
- Leader election (for HA)
- Health probes
- Metrics server

### High Availability (HA) and Leader Election

**The Problem**: If you run just one operator pod and it dies, nobody is watching your cluster.

**The Solution — Leader Election**:

```
+-----------------------------------------------------------+
|                    Kubernetes Cluster                     |
|                                                           |
|  +--------------+  +--------------+  +--------------+     |
|  |  KubeCure    |  |  KubeCure    |  |  KubeCure    |     |
|  |  Replica 1   |  |  Replica 2   |  |  Replica 3   |     |
|  |              |  |              |  |              |     |
|  |  LEADER      |  |  (standby)   |  |  (standby)   |     |
|  |  (active)    |  |              |  |              |     |
|  +--------------+  +--------------+  +--------------+     |
|         |                                                 |
|         | does all the work                               |
|         v                                                 |
|  +--------------------------------------------------+     |
|  |              Kubernetes API Server               |     |
|  +--------------------------------------------------+     |
+-----------------------------------------------------------+
```

**How it works**:
1. All replicas try to acquire a "lease" (lock) in Kubernetes
2. Only ONE wins and becomes the **leader**
3. The leader does all the actual work
4. Others watch and wait
5. If leader dies, another replica instantly takes over

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

## 6. Operator SDK and Controller Runtime

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
operator-sdk init --domain kubecure.io --repo github.com/adityax25/KubeCure

# Create a new controller for an existing resource
operator-sdk create api --group core --version v1 --kind Pod --controller=true --resource=false
```

### Generated Project Structure

After running `operator-sdk init`, you get:

```
kubecure/
+-- cmd/main.go           # Entrypoint (starts Manager)
+-- config/               # K8s manifests to deploy the operator
+-- internal/controller/  # Your controller code
+-- go.mod, go.sum        # Go dependencies
+-- Makefile              # Build commands
+-- Dockerfile            # Container recipe
+-- bin/manager           # Compiled binary (not in git)
+-- .github/workflows/    # CI/CD pipelines
```

### The cmd/main.go File

This is the entrypoint that:
1. Initializes the Scheme (teaches Go about Kubernetes types)
2. Creates the Manager
3. Registers controllers with the Manager
4. Starts the Manager

Key sections:
- **Scheme**: Registers Kubernetes types so Go can serialize/deserialize them
- **Manager creation**: Sets up client, cache, leader election, metrics
- **Controller registration**: Wires your controllers into the Manager
- **Health checks**: `/healthz` and `/readyz` endpoints

---

## 7. Failure Detection Implementation

### Pod Failure Types

KubeCure detects these failure states:

| Failure Type | Description | When It Triggers |
|--------------|-------------|------------------|
| `CrashLoopBackOff` | Container keeps crashing | App crashes on startup, runtime errors |
| `ImagePullBackOff` | Can't pull container image | Typo in image name, private registry auth |
| `OOMKilled` | Out of memory | Memory limit too low for application |
| `CreateContainerConfigError` | Bad container config | Missing Secret or ConfigMap |
| `RunContainerError` | Runtime failure | Bad entrypoint, permissions issue |
| `Evicted` | Node resource pressure | Disk or memory pressure on node |
| `Error` | Generic container error | Container exited with non-zero code |
| `Unknown` | Catch-all | Any other unrecognized failure |

### Where Failures Appear in Pod Status

Failures can appear in different places in the Pod status:

| Location | Accessed Via | Failure Types Found |
|----------|-------------|---------------------|
| Container Waiting State | `pod.Status.ContainerStatuses[].State.Waiting.Reason` | CrashLoopBackOff, ImagePullBackOff, CreateContainerConfigError |
| Container Terminated State | `pod.Status.ContainerStatuses[].State.Terminated.Reason` | OOMKilled, Error |
| Pod Reason | `pod.Status.Reason` | Evicted |
| Pod Phase | `pod.Status.Phase` | Failed |

### The Detection Flow

When a pod changes:

```
1. Pod crashes on some node
         |
         v
2. kubelet on node reports to API Server
         |
         v
3. API Server updates etcd: "Pod status = CrashLoopBackOff"
         |
         v
4. KubeCure's informer (watching via API Server) gets notified
         |
         v
5. Reconcile() is called with that Pod's name/namespace
         |
         v
6. KubeCure reads Pod details, calls detectFailure()
         |
         v
7. If failure found, log it (future: send to AI, create PR)
```

### Skipping System Namespaces

We skip pods in system namespaces to avoid noise:
- `kube-system` — Core Kubernetes components
- `kube-public` — Public cluster info
- `kube-node-lease` — Node heartbeats
- `local-path-storage` — kind-specific storage provisioner

---

## 8. LLM Integration Patterns

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

## 9. GitOps Principles

### What is GitOps?

**Technical**: An operational framework where Git repositories are the single source of truth for declarative infrastructure and applications. Changes are made via Pull Requests, and automated systems sync the Git state to the target environment.

**Intuitive**: Instead of `kubectl apply` manually, you commit YAML to Git. A robot watches Git and applies changes for you. Every change is reviewed, versioned, and auditable.

### The GitOps Flow

```
Developer       GitHub          ArgoCD/Flux       Kubernetes
    |              |                 |                |
    |--(1) Push--->|                 |                |
    |              |<--(2) Detect----|                |
    |              |     change      |                |
    |              |                 |--(3) Sync----->|
    |              |                 |                |
    |<------------------ (4) Deployed ----------------+
```

### KubeCure's Role in GitOps

KubeCure is **not** a GitOps operator (like ArgoCD). Instead, it:

1. Detects failures in the cluster
2. Proposes fixes by creating **Pull Requests** in the source repo
3. Lets humans (or automated pipelines) merge and deploy

This maintains the GitOps principle: **Git is the source of truth**, not the cluster.

| KubeCure Action | GitOps Compliance |
|-----------------|-------------------|
| Create PR with fix | Change goes through Git |
| Auto-apply to cluster | Bypasses Git (anti-pattern) |
| Create Issue for review | Human in the loop |

---

## 10. Terraform and Infrastructure as Code

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

### Hybrid Approach: Local + Cloud

| Phase | Environment | Tool | Purpose |
|-------|-------------|------|---------|
| Development | Local | kind | Fast iteration, no cost |
| Staging/Demo | AWS | Terraform + EKS | Real cloud behavior |
| CI Tests | GitHub Actions | kind | Automated testing |

---

## 11. Observability Stack

### The Three Pillars

| Pillar | What It Captures | Tool |
|--------|------------------|------|
| **Metrics** | Numeric time-series data (counters, gauges, histograms) | Prometheus |
| **Logs** | Discrete events with context | stdout, Loki, or CloudWatch |
| **Traces** | Request flow across services | Jaeger, OpenTelemetry |

### Prometheus + Grafana for KubeCure

```
+------------+        +------------+        +------------+
|  KubeCure  |------->| Prometheus |<------>|  Grafana   |
| (exposes   | scrape | (stores    | query  |(visualize) |
|  /metrics) |        |  metrics)  |        |            |
+------------+        +------------+        +------------+
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
)
```

### Structured Logging

Instead of:
```go
log.Println("pod failed")
```

Use structured logging:
```go
logger.Info("pod failure detected",
    "pod", pod.Name,
    "namespace", pod.Namespace,
    "failureType", failure.FailureType,
    "restartCount", cs.RestartCount,
)
```

**Why?** — Structured logs are queryable. You can search for `namespace="production" AND restartCount>5`.

---

## 12. Frontend Architecture

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

For real-time failure feed, WebSockets or SSE are preferred.

---

## Git Best Practices

### Conventional Commits

Format: `<type>(<scope>): <description>`

| Type | When to Use |
|------|-------------|
| `feat` | New feature |
| `fix` | Bug fix |
| `docs` | Documentation only |
| `chore` | Maintenance (deps, configs) |
| `refactor` | Code change without new feature/fix |
| `test` | Adding tests |
| `ci` | CI/CD changes |

Example:
```bash
git commit -m "feat(controller): add failure detection for CrashLoopBackOff"
```

### What to Ignore (.gitignore)

- `bin/` — Compiled binaries
- `.env` — Secrets
- `.DS_Store` — macOS junk
- `*.test` — Test binaries
- `cover.out` — Coverage reports
