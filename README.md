# KubeCure

**An AI-Native Autonomous Kubernetes Self-Healing Engine**

## What is KubeCure?

KubeCure extends the Kubernetes Control Plane to **autonomously detect, diagnose, and remediate cluster failures** using LLM-driven GitOps workflows. Think of it as an AI-powered SRE that never sleeps, it is continuously watching your cluster, understanding failures in context, and proposing intelligent fixes via Pull Requests.

### The Problem

Modern Kubernetes clusters fail in complex, unpredictable ways:

- `CrashLoopBackOff` from misconfigured environment variables
- `OOMKilled` due to insufficient resource limits  
- `ImagePullBackOff` from typos in image tags
- Application crashes buried in cryptic log traces

Engineers spend countless hours context-switching between logs, YAML manifests, and cluster events to diagnose issues that often have simple fixes. This **Mean Time To Recovery (MTTR)** is where KubeCure steps in.

### The Solution

KubeCure acts as an **intelligent intermediary** between your failing workloads and your GitOps repository:

```
                              KubeCure Architecture

   +--------------+          +--------------+          +--------------+
   |  Kubernetes  |  watch   |   KubeCure   |  reason  |  Gemini AI   |
   |   Cluster    | -------> |  Controller  | -------> |    (LLM)     |
   |              |          |              | <------- |              |
   +--------------+          +--------------+   fix    +--------------+
          |                         |
          | events, logs            | PR / Issue
          | manifests               |
          v                         v
   +--------------+          +--------------+
   |   Failing    |          |    GitHub    |
   |     Pod      |          |  Repository  |
   +--------------+          +--------------+
```

---

## How It Works

KubeCure operates as a **Kubernetes Operator** using the standard reconciliation loop pattern:

### 1. Observe — The Sensor Layer

The controller watches the Kubernetes API for `Pod` and `Event` resources, filtering for terminal failure states like `CrashLoopBackOff`, `ImagePullBackOff`, `OOMKilled`, and others.

### 2. Aggregate — Context Collection

Upon detecting a failure, KubeCure gathers diagnostic context:

| Context Type | Description |
|--------------|-------------|
| **Live Logs** | Recent lines of `stdout/stderr` from the failing container |
| **Manifests** | Current YAML configuration (env vars, resource limits, image tags) |
| **Events** | Relevant warnings from the Kubernetes scheduler |

### 3. Reason — The AI Brain

The aggregated context is sent to **Gemini AI** with a structured prompt. The LLM returns a diagnosis including root cause analysis, suggested fix, and confidence score.

### 4. Remediate — GitOps Integration

Based on the confidence score:

| Confidence | Action |
|------------|--------|
| **High (>=80)** | Create a **Pull Request** with the fix to the source repository |
| **Low (<80)** | Open a **GitHub Issue** with the diagnostic report for human review |

### 5. Observe — Telemetry

All actions are instrumented and exported to Prometheus/Grafana for observability.

---

## Planned Architecture

```
kubecure/
├── cmd/                    # Application entrypoints
├── internal/               # Private application code
│   ├── controller/         # Reconciliation logic
│   ├── detector/           # Failure detection
│   ├── aggregator/         # Context collection
│   ├── ai/                 # LLM integration
│   └── remediation/        # GitOps handlers
├── pkg/                    # Shared libraries
├── api/                    # CRD definitions
├── config/                 # Kubernetes manifests
├── terraform/              # Infrastructure as Code
└── web/                    # Frontend dashboard
```

### Design Principles

- **Clean Architecture**: Decoupled layers with dependency injection
- **Interface-Driven AI**: Swappable LLM providers (Gemini, GPT, Claude)
- **Idempotent Reconciliation**: Safe to run repeatedly without side effects
- **Observability-First**: Structured logging with correlation IDs

---

## Tech Stack

| Layer | Technology |
|-------|------------|
| **Backend** | Go, `operator-sdk`, `controller-runtime` |
| **AI Engine** | Google Gemini API |
| **Infrastructure** | AWS EKS, Terraform |
| **State** | Redis |
| **GitOps** | GitHub REST API |
| **Frontend** | React, TypeScript, Framer Motion, Tailwind CSS |
| **Observability** | Prometheus, Grafana |

---

## Status

This project is under active development. Documentation will be expanded as components are implemented.