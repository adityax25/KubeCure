# Failure scenarios

Deliberately broken workloads, one per failure mode. Each is engineered to fail in exactly one way,
so detection can be demonstrated live and real pod statuses can be captured as test fixtures.

All scenarios are applied to the `kubecure-demo` namespace and labelled
`kubecure.io/scenario=<name>`.

## Usage

```bash
make scenarios-apply                        # apply all
make scenarios-apply SCENARIO=oomkilled     # apply one
make scenarios-status                       # inspect current state
make scenarios-capture SCENARIO=oomkilled   # record a fixture
make scenarios-delete                       # remove the namespace and everything in it
```

## Coverage

Six of the eight classified failure modes are reproducible on a single node local cluster.

| Scenario | Mechanism | Expected classification | Surfaces after |
| :- | :- | :- | :- |
| `crashloopbackoff` | container exits non-zero on start | `CrashLoopBackOff` | ~20s |
| `oomkilled` | allocates 200Mi against a 32Mi limit | `OOMKilled` | ~30s |
| `imagepullbackoff` | image tag does not exist | `ImagePullBackOff` | ~30s |
| `createcontainerconfigerror` | references a missing ConfigMap | `CreateContainerConfigError` | immediate |
| `unschedulable` | requests 900Gi of memory | `Unschedulable` | 30s grace |
| `probefailure` | readiness probe targets a closed port | `ProbeFailure` | 35s |

### Not reproducible locally

| Mode | Why |
| :- | :- |
| `Evicted` | Requires genuine kubelet node pressure. Forcing it on a single node cluster risks destabilising the control plane running on that same node. The eviction API produces a deletion rather than the `Evicted` pod status that node pressure produces. |
| `RunContainerError` | Reproduction depends on container runtime specifics and is not consistent enough to rely on for a demonstration. |

Both are classified by the detector and covered by unit test fixtures. Neither is claimed as
demonstrable.

## Why the OOM scenario matters most

A container killed for exceeding its memory limit is restarted, and the cluster then displays
`CrashLoopBackOff`. The original cause survives only in the container's previous termination record.

This scenario is therefore the live proof of ADR-013: `kubectl get pods` shows `CrashLoopBackOff`,
and the detector must report `OOMKilled`. The difference between those two outputs is the difference
between reporting a symptom and reporting a cause.

## Capturing fixtures

`make scenarios-capture SCENARIO=<name>` writes the failing pod's full status to
`test/fixtures/<name>.json`.

Hand written fixtures prove the classification logic is internally consistent. Captured fixtures
prove it matches what the cluster actually emits, which is a different and stronger claim.
