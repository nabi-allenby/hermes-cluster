# hermes-cluster

Hermes-as-a-Service on Kubernetes: run personal [Hermes agents](https://github.com/NousResearch/hermes-agent)
as managed, persistent, scale-to-zero sessions. This repo builds the platform's
**lifecycle-manager** — a small, stateless Go service that orchestrates agent
sessions as [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)
`SandboxClaim`s — plus the Helm chart and Terraform that will ship around it.

The heavy lifting is delegated:

- **Session lifecycle** — agent-sandbox (adopted unmodified, pinned): one
  stateful pod per session, PVC-backed home directory, `operatingMode`
  suspend/resume, warm pools, cascade deletion.
- **Messaging** — [hermes-relay-connector](https://github.com/nabi-allenby/hermes-relay-connector)
  (external pinned image): owns the Discord bot token, buffers messages
  durably for suspended agents, pokes this service's `/wake/{session}` when
  buffered work arrives. **Optional** — the lifecycle-manager is a generic
  session orchestrator without it.

```
Discord ⇄ hermes-relay-connector ──GET /wake/{id}──▶ lifecycle-manager
                 ▲    ▲                                   │ claims CRUD · operatingMode
                 │    └──── admin API (provision, ────────┤ patches · sweepers
        WS+HMAC dial        activity, deprovision)        ▼
            session pods (hermes gateway, PVC home)   Kubernetes API + agent-sandbox
```

## Status

- ✅ lifecycle-manager: session CRUD API, `/wake`, idle + TTL sweepers,
  connector integration (provision / activity / deprovision / routes),
  reconcile report. Unit + e2e tested (minikube).
- ✅ P-M1 headless Hermes pod recipe: the real hermes-agent as a sandbox
  session — PVC home, first-boot seed, boot-time self-provision, graceful
  suspend, re-auth on resume ([docs/p-m1.md](docs/p-m1.md), `make p-m1`).
- ✅ **P-AC1 passed live** (`hack/p-ac1/`): real Discord DM → in-cluster
  agent replies; idle → suspended to disk-only; DM while suspended →
  buffered → wake poke → resume → reply. Decommission cascades in seconds.
- ✅ Helm chart (`charts/hermes-platform`) — the whole platform as one
  release, lifecycle drill verified on minikube; Terraform `aks` + `platform`
  modules and the `aks-personal` example (`terraform/`, validate-clean).
- ✅ **P-AC4 passed live on AKS** ([docs/p-ac4.md](docs/p-ac4.md)): terraform +
  two secrets + chart → Discord conversation, idle suspend, wake-on-message
  (message→connected ~22 s; managed-csi attach ≈ 15 s — §5.2 measured).
- ⬜ GHCR package visibility + OCI chart publish, P-M4 (Spot eviction drill,
  EKS module) — next.

## Quickstart (minikube)

```bash
make minikube-up      # start minikube + install agent-sandbox v0.5.4
make p-m0             # sanity: claim → suspend → PVC survives → resume
kubectl apply -f hack/e2e/template.yaml   # a SandboxTemplate + SandboxWarmPool
HLM_WARM_POOL=e2e-pool make run-local     # run the lifecycle-manager locally
```

Then:

```bash
curl -X POST localhost:8080/v1/sessions -d '{"id":"my-agent"}'
curl localhost:8080/v1/sessions/my-agent          # state: Waking → Ready
curl localhost:8080/wake/my-agent                 # idempotent resume
curl -X DELETE localhost:8080/v1/sessions/my-agent
```

Full API: [docs/api.md](docs/api.md).

## How it works

One session = one `SandboxClaim` = one `Sandbox` = one PVC (the agent's entire
home). The lifecycle-manager is **stateless**: per-session knobs live as
annotations on the claim; live state is derived on read from the sandbox
(`Running`+Ready ⇒ `Ready`, `Suspended`+confirmed ⇒ `Suspended`, …) and never
stored. Restarting it loses nothing.

Two sweepers are the only writers of lifecycle intent:

- **Idle sweeper** (connector mode only): polls per-instance activity from the
  connector admin API and suspends sessions that are Ready, quiet past their
  idle timeout, with no turn in flight and nothing buffered. Unknown activity
  (connector restart) is never treated as idle.
- **TTL sweeper**: decommissions expired sessions through the same single
  code path as `DELETE /v1/sessions/{id}` — connector purge first, then claim
  deletion (cascades pod + PVC). `spec.lifecycle` on claims is never used.

The wake loop (with the connector): message to a suspended agent → connector
buffers durably → pokes `GET /wake/{id}` (payload-free, unauthenticated,
cooldown-limited) → lifecycle-manager patches `operatingMode: Running` → pod
recreates on the same PVC → gateway re-dials → backlog drains in order.
A lost poke degrades to delivery-on-next-resume, never loss.

## Configuration (env)

| Var | Default | Notes |
|---|---|---|
| `HLM_LISTEN` | `:8080` | HTTP listener |
| `HLM_NAMESPACE` | in-cluster / `default` | namespace for claims |
| `HLM_WARM_POOL` | **required** | default `SandboxWarmPool` for new sessions (v1beta1 claims reference pools; a `replicas: 0` pool = cold-start everything) |
| `HLM_SANDBOX_API_GROUP` / `HLM_SANDBOX_EXT_API_GROUP` / `HLM_SANDBOX_API_VERSION` | `agents.x-k8s.io` / `extensions.agents.x-k8s.io` / `v1beta1` | CRD pin knobs |
| `HLM_SWEEP_INTERVAL` | `60s` | sweep cadence (jittered) |
| `HLM_IDLE_TIMEOUT` | `30m` | global idle timeout; `0` disables |
| `HLM_TTL` | `0` | global session TTL; `0` disables |
| `HLM_API_TOKEN(_FILE)` | unset | optional bearer on `/v1/*` (never `/wake`/health) |
| `HLM_CONNECTOR_ENABLED` | `false` | enable the relay-connector integration |
| `HLM_CONNECTOR_URL` | — | connector **admin-plane** base URL (no `/healthz` there when `HRC_ADMIN_LISTEN` splits planes; reachability is probed via the unauthenticated `/metrics`) |
| `HLM_CONNECTOR_ADMIN_TOKEN(_FILE)` | — | bearer for `/admin/v1/*` |
| `HLM_CONNECTOR_PROVISION_TOKEN(_FILE)` | — | bearer for `POST /relay/provision`; unset ⇒ sessions rely on agent self-provision |
| `HLM_CONNECTOR_BOT_ID` / `HLM_CONNECTOR_PLATFORM` | — / `discord` | provision parameters |
| `HLM_WAKE_BASE_URL` | — | base for registered wake URLs (`<base>/wake/<id>`) |
| `HLM_ORPHAN_POLICY` | `report` | claims↔instances drift is reported in `/status`, never auto-deleted |
| `HLM_LOG_LEVEL` / `HLM_LOG_FORMAT` | `info` / `json` | logging |

Durations accept Go syntax (`30m`) or bare seconds (`1800`). Every `*_FILE`
variant wins over its plain twin. Local runs use `KUBECONFIG`/`~/.kube/config`.

Connector-client discipline (encoded in `internal/connector`): no retries
anywhere — the connector throttles 10 auth failures per source IP per 60 s
with 429s that hit good credentials too, so any 429 short-circuits all calls
for 65 s and the sweep cadence is the retry loop.

## Development

```bash
make test        # unit tests
make e2e         # e2e vs minikube (+ docker for the connector tier)
make image       # ghcr.io/nabi-allenby/hermes-cluster/lifecycle-manager
```

e2e tiers: **0** — agent-sandbox suspend/resume substrate (`hack/p-m0/run.sh`);
**1** — session CRUD/wake/TTL/statelessness, no connector; **2** — real
connector container, agentless full wake loop via its echo API (provision →
suspend → buffered echo message → wake poke → resume), deprovision purge,
orphan reporting. Tier 2 needs the connector's full admin surface (v0.2.0+):
it picks `$HLM_E2E_CONNECTOR_IMAGE`, a locally built `hrc:e2e`, or the GHCR
`v0.2.0` image, and skips when none is available.

## Pins

| Dependency | Version | Where |
|---|---|---|
| agent-sandbox | `v0.5.4` | `hack/minikube-up.sh` (`AGENT_SANDBOX_VERSION`); facts in [docs/p-m0.md](docs/p-m0.md) |
| hermes-relay-connector | `v0.2.0` | full admin surface (activity, PATCH, routes CRUD, deprovision purge) + non-root image, graceful SIGTERM, `/metrics`; contract notes in `internal/connector` |
| hermes-agent | `@244d296` | consumed indirectly via the connector's conformance gate |

Design: the definitive `hermes-cluster` design doc (external); connector
internals live in the hermes-relay-connector repo only.
