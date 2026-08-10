# P-M0 — substrate facts: agent-sandbox pinned at v0.5.4

Everything the lifecycle-manager assumes about kubernetes-sigs/agent-sandbox, read off the
**v0.5.4** release (2026-07-30) manifest (`sandbox-with-extensions.yaml`) and controller source
at tag `v0.5.4`. If the pin moves, re-verify this file first — all field paths below are
encoded in `lifecycle-manager/internal/k8s/unstructured.go` and nowhere else.

## Install

One manifest carries everything (namespace, CRDs, controller, conversion webhook):

```
https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.4/sandbox-with-extensions.yaml
```

(`sandbox.yaml` + `extensions.yaml` are the split variants; the combined file is what
`hack/minikube-up.sh` applies.) Controller runs in `agent-sandbox-system`. The
SandboxClaim CRD uses a **conversion webhook** — claims cannot be created until the
controller pod is Ready and the webhook has endpoints.

## API groups and versions

| Kind | Group | Version used | Notes |
|---|---|---|---|
| `Sandbox` | `agents.x-k8s.io` | `v1beta1` (storage) | `v1alpha1` still served |
| `SandboxClaim` | `extensions.agents.x-k8s.io` | `v1beta1` (storage) | **not field-compatible with v1alpha1** |
| `SandboxTemplate` | `extensions.agents.x-k8s.io` | `v1beta1` (storage) | |
| `SandboxWarmPool` | `extensions.agents.x-k8s.io` | `v1beta1` (storage) | |

Two groups: the core group holds only `Sandbox`; the claim/template/warmpool trio live in
the `extensions.` prefix. RBAC must name both groups.

## SandboxClaim v1beta1 — the shape that matters

- `spec.warmPoolRef.name` (**required**) — the only way a claim names its source. There is
  no `sandboxTemplateRef` on v1beta1 claims; the template is referenced by the pool
  (`SandboxWarmPool.spec.sandboxTemplateRef.name`).
- **Cold start works with an empty pool**: when no adoptable spare exists, the claim
  controller resolves pool → template and creates the Sandbox from the template directly
  (`sandboxclaim_controller.go` "Cold path"). A `replicas: 0` pool is therefore a valid
  "no warm spares, cold-start everything" configuration.
- `spec.additionalPodMetadata.{labels,annotations}` — per-claim pod metadata (the only
  per-session customization we use).
- `spec.env[]` — subject to the template's `envVarsInjectionPolicy`; we set it `Disallowed`
  in templates and never use claim env (design §4.3).
- `spec.lifecycle.{shutdownPolicy(default Retain|Delete|DeleteForeground), shutdownTime,
  ttlSecondsAfterFinished}` — **never set by the lifecycle-manager** (design §4.4: TTL has
  exactly one code path, ours).
- `status.conditions[]` — standard metav1 conditions; type `Ready` mirrors the bound
  Sandbox's `Ready` condition (reasons seen in source: `WarmPoolNotFound`,
  `TemplateNotFound`, `AdoptionConflict`, `SandboxNotReady`, `ReconcilerError`, ...).
- `status.sandbox.name` — the bound Sandbox's name. **On warm-pool adoption this differs
  from the claim name** (spares are named `<pool>-<suffix>`); on cold start it equals the
  claim name. Always resolve the sandbox through `status.sandbox.name`, falling back to
  the claim name only while status is unset.

## Sandbox v1beta1

- `spec.operatingMode`: `Running` | `Suspended`, default `Running`. Suspend = patch this
  field; the controller deletes the pod, keeps PVCs and Service, and flipping back to
  `Running` recreates the pod on the same PVCs. The claim controller never writes
  `operatingMode` (it only reconciles pod-template metadata), so the lifecycle-manager's
  merge patch does not fight it.
- `status.conditions[]`: type `Ready` (reasons `DependenciesReady`/`DependenciesNotReady`/
  `SandboxSuspended`), type `Suspended` (reasons `PodTerminated`, `PodTerminating`,
  `PodNotTerminated`, `PodNotOwned`, `PodStateUnknown`, `NotSuspended`), type `Finished`
  (terminal pod phase; reasons `PodSucceeded`/`PodFailed`).
- `status.serviceFQDN`, `status.podIPs` — stable in-cluster address of the session.

## Ownership / cascade

`SandboxClaim` → controller-owns → `Sandbox` → controller-owns → Pod, Service, and PVCs.
PVCs are created as `<volumeClaimTemplate.name>-<sandboxName>` with an owner reference to
the Sandbox. Deleting the claim therefore cascades the sandbox, pod, service **and PVCs**
(P-AC3's cascade requirement) via normal garbage collection.

## Lifecycle-manager RBAC (both groups)

| Group | Resource | Verbs |
|---|---|---|
| `extensions.agents.x-k8s.io` | `sandboxclaims` | get, list, watch, create, delete, patch |
| `agents.x-k8s.io` | `sandboxes` | get, list, watch, patch |

Nothing else — no pods, secrets, or PVCs (design §4.2).

## Derived session state (design §4.3), from the two objects

| operatingMode | Sandbox conditions | Derived state |
|---|---|---|
| — (no sandbox bound yet) | — | `Pending` |
| `Running` | `Ready=True` | `Ready` |
| `Running` | `Ready` not True | `Waking` (covers first provision too) |
| `Suspended` | `Suspended=True` | `Suspended` |
| `Suspended` | `Suspended` not True | `Suspending` |
| any | claim `deletionTimestamp` set | `Terminating` |

## Timings (minikube, docker driver, hostpath storage; `hack/p-m0/run.sh`, 2026-08-10)

| Step | Measured |
|---|---|
| claim create → sandbox Ready (cold start, image cached) | **2.4 s** |
| operatingMode Suspended → pod gone, Suspended=True | **31.8 s** — dominated by the 30 s termination grace period (busybox `sleep` ignores SIGTERM); a well-behaved agent image exits promptly |
| operatingMode Running → Ready=True (resume, PVC reattach) | **2.3 s** |
| claim delete → sandbox + PVC garbage-collected | ~10–60 s (async GC; poll, don't assume immediate) |

The resume figure is the k8s half of the wake path: on this substrate the design's
≤30 s wake target has ~27 s of headroom before agent boot time.

> Cloud CSI attach latency (the design's open wake-budget variable, §5.2) is **not**
> measurable on minikube's hostpath storage — that number still needs the throwaway AKS
> run from the design doc.
