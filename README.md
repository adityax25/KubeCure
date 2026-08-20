# <img src="https://raw.githubusercontent.com/kubernetes/kubernetes/master/logo/logo.png" alt="Kubernetes" width="22"/> KubeCure

**An AI-native, Go-based autonomic self-healing engine for Kubernetes.**

Kubernetes will restart a broken container indefinitely without ever establishing why it broke.
KubeCure adds the missing step: it detects pod failures, gathers the evidence a human engineer
would gather, produces a root cause hypothesis, and proposes a concrete fix as a reviewable
change.

> **Status: early development.** The [Current Status](#current-status) section tracks exactly
> what is built versus planned. Nothing in this document describes capability that does not exist.

## The Problem

Kubernetes self-healing is deliberately mechanical. When a container exits, the platform restarts
it. When it keeps exiting, the platform waits longer between attempts and keeps restarting it.
This is correct behavior and it is also the entire extent of the platform's reasoning.

A container killed for exceeding a memory limit that was set too low will be restarted forever,
and the limit will never be questioned. The information needed to diagnose the failure is present
in the cluster the whole time, spread across container statuses, events, logs, and the owning
workload's specification. Assembling it is manual work, and that work is what stands between a
failure and its resolution.

## The Solution

KubeCure treats each detected failure as a first-class Kubernetes object with an explicit
lifecycle, and advances it through a pipeline of independent controllers:

```
   detect  ->  collect evidence  ->  analyze  ->  remediate  ->  verify
```

Each stage records its result and a timestamp on the object, so the time from detection to
diagnosis, and from detection to verified recovery, are measured values rather than estimates.

## Architecture

```
                    ┌────────────────────────┐
                    │  Kubernetes API Server │
                    └───────────┬────────────┘
                                │  watch: Pods, Events
                                ▼
╔══════════════════════════════════════════════════════════════════════╗
║                          KubeCure Operator                           ║
║                                                                      ║
║  ┌────────────┐   ┌────────────┐   ┌────────────┐   ┌────────────┐   ║
║  │  Detector  │──▶│  Evidence  │──▶│  Analysis  │──▶│Remediation │   ║
║  │ Controller │   │ Controller │   │ Controller │   │ Controller │   ║
║  └─────┬──────┘   └─────┬──────┘   └─────┬──────┘   └─────┬──────┘   ║
╚════════╪════════════════╪════════════════╪════════════════╪══════════╝
         │                │                │                │
         ▼                ▼                ▼                ▼
┌──────────────────────────────────────────────────────────────────────┐
│                   Diagnosis  (custom resource)                       │
│                                                                      │
│  Detected ───▶ Enriched ───▶ Diagnosed ───▶ Remediating ───▶ Healed  │
│     t0            t1             t2              t3            t4    │
└──────────────────────────────────────────────────────────────────────┘
```

| Controller | Watches | Responsibility |
| :- | :- | :- |
| **Detector** | Pods, Events | Classifies failure signals and opens a `Diagnosis` for each distinct failure signature. |
| **Evidence** | Diagnoses at `Detected` | Collects container logs, cluster events, and the owning workload spec, then redacts sensitive values. |
| **Analysis** | Diagnoses at `Enriched` | Produces a root cause and a typed remediation action, via the rule engine or an LLM. |
| **Remediation** | Diagnoses at `Diagnosed` | Applies the action as a cluster patch or a pull request, then verifies that the workload recovered. |

The controllers never call one another. Each watches for objects in the single phase it owns,
performs its transformation, and advances the phase, and that write is what wakes the next stage.
This is the same coordination pattern Kubernetes uses internally between the Deployment,
ReplicaSet, and scheduler controllers.

The separation exists so that each stage carries its own concurrency and retry policy. Analysis is
rate limited and costly and requires long backoff; detection is local and cheap. Combined into one
reconciler, an analyzer outage would throttle failure detection. Separated, diagnoses queue at
`Enriched` while detection continues normally.

## The GitOps Loop

In pull request mode the operator never mutates the cluster. It commits the proposed fix to the
repository that holds the workload manifests and opens a pull request describing the diagnosis.
A continuous delivery controller applies the change only once a human merges it.

```
   +-------------+   detects and     +------------------+
   |  cluster    |   diagnoses       |    KubeCure      |
   |  (failing   | ----------------> |    operator      |
   |   workload) |                   +------------------+
   +-------------+                            |
         ^                                    | opens a pull request
         |                                    | containing the fix and
         |                                    | the root cause analysis
         |                                    v
         |                            +------------------+
         |                            |     GitHub       |
         |                            |  manifests repo  |
         |                            +------------------+
         |                                    |
         |                                    | human review and merge
         |                                    v
         |   applies the merged      +------------------+
         +---------------------------|      ArgoCD      |
             desired state           +------------------+
```

This makes every automated change auditable and reversible through version control, at the cost of
recovery latency that depends on human review. Direct mode skips the loop entirely and patches the
cluster, which is faster and is the only mode that can produce a true end to end recovery time.
Both modes are gated by a confidence threshold and an explicit whitelist of permitted actions.

## Design Decisions

| Decision | Rationale |
| :- | :- |
| **Failures are custom resources, not log lines** | Gives the operator a real API surface (`kubectl get diagnoses`), durable state that survives restarts, deduplication by failure signature, and per-stage timestamps for honest measurement. |
| **Analysis sits behind an interface** | A deterministic rule-based analyzer and an LLM analyzer implement the same contract. The product remains functional with no API key and no network, and the rule-based path provides a baseline to measure the LLM against. |
| **Two remediation modes** | Pull request mode is auditable and human gated. Direct mode patches the cluster and is the only mode that can produce a true end to end recovery time. |
| **Evidence is redacted before it leaves the cluster** | Container logs routinely contain tokens, connection strings, and personal data. Scrubbing is a requirement, not a refinement. |
| **Remediation is verified** | After a fix is applied, the operator confirms the workload actually recovered and records the outcome. Without verification the system guesses rather than heals. |
| **Configuration is a custom resource** | A namespaced policy object lets one operator serve multiple teams with different failure scopes, remediation modes, and confidence thresholds. |

## Tech Stack

| Layer | Technology |
| :- | :- |
| **Operator** | Go, `controller-runtime`, Operator SDK |
| **Local cluster** | kind |
| **Analysis** | pluggable: rule-based, or an LLM provider |
| **GitOps** | GitHub API for pull requests, ArgoCD for continuous delivery |
| **CI/CD** | GitHub Actions |
| **Observability** | Prometheus metrics, Grafana |
| **Packaging** | Docker, Helm |
| **Cloud (optional)** | Terraform, AWS EKS |

## Current Status

### Completed

- Repository initialized

### In Progress

- Project scaffolding and custom resource definitions

### Upcoming

- Failure detection across a defined set of pod failure modes
- A reproducible failure injector, one deliberate broken workload per mode
- Evidence collection: logs, events, owner references, with redaction
- Rule-based analyzer, then LLM-backed analysis
- Remediation in dry run, pull request, and direct modes, with verification
- ArgoCD wired to a manifests repository to close the GitOps loop
- GitHub Actions for build, test, lint, and image publishing
- Prometheus metrics and a measurement harness for detection and recovery timing
- Helm packaging and least privilege RBAC

## Getting Started

### Prerequisites

- Go 1.24 or later
- Docker
- kubectl
- kind
- Operator SDK

### Local cluster

```bash
kind create cluster --name kubecure-dev
kubectl get nodes
```

Further instructions will be added as the operator becomes runnable.

## License

Apache License 2.0.
