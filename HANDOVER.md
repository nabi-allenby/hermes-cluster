# HANDOVER — hermes-cluster lifecycle-manager (orchestrator)

Everything a new maintainer (human or agent) needs to pick this project up.
Started 2026-08-10; updated through the 2026-08-11 library split.

---

## 1. What this is

The **lifecycle-manager** is the orchestrator of the hermes-cluster platform:
a small, stateless Go HTTP service that manages personal
[Hermes agent](https://github.com/NousResearch/hermes-agent) sessions as
Kubernetes workloads. One session = one persistent, stateful agent with its
whole home directory on a PVC, suspendable to disk-only cost and wakeable by
an incoming Discord message.

It deliberately owns very little. The two hard problems are delegated:

- **Session lifecycle** → [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)
  (adopted unmodified, pinned **v0.5.4**): `SandboxClaim`/`SandboxTemplate`/
  `SandboxWarmPool`/`Sandbox` CRDs give single-stateful-pod semantics, PVC
  survival, `operatingMode: Running|Suspended`, warm pools, cascade delete.
- **Messaging** → [hermes-relay-connector](https://github.com/nabi-allenby/hermes-relay-connector)
  (external pinned image, **v0.2.0**): owns the Discord bot token, buffers
  messages durably for suspended agents, pokes our `/wake/{session}` when
  buffered work arrives. **Optional** — without it the lifecycle-manager is a
  generic session orchestrator (no idle detection, no messaging).

What the orchestrator itself does: session CRUD over HTTP, the wake endpoint,
an idle sweeper and a TTL sweeper (the **only** writers of lifecycle intent in
the platform), the connector admin-API client, and a claims↔instances
reconcile report.

Authoritative design: `~/Downloads/hermesclusterDESIGN.md` (definitive,
2026-08-10). Its §6.3 connector work items C-1…C-8 are ALL shipped in
connector v0.2.0. Connector internals live in that repo only — this repo
consumes its published image over HTTP, zero code coupling.

## 2. Repo map

```
hermes-cluster/                        github.com/nabi-allenby/hermes-cluster (library repo, private for now)
├── README.md                          quickstart, env table, pins
├── HANDOVER.md                        this file
├── Makefile                           build/test/e2e/minikube targets
├── .github/workflows/ci.yml           vet+unit, multi-arch image→GHCR, minikube e2e
├── charts/  terraform/                placeholders (P-M3/P-M4), READMEs describe intent
├── docs/
│   ├── api.md                         full HTTP API reference
│   ├── p-m0.md                        ⭐ agent-sandbox substrate facts + measured timings
│   └── p-m1.md                        ⭐ headless Hermes pod recipe + measured timings
├── hack/
│   ├── minikube-up.sh                 minikube + pinned agent-sandbox install
│   ├── p-m0/                          hello-world template/pool/claim + run.sh (tier-0 e2e)
│   ├── p-m1/                          real-agent template, dev connector, seed tooling, drill
│   └── e2e/                           rbac.yaml (exact LM RBAC), template.yaml (e2e pool)
└── lifecycle-manager/                 Go module (…/lifecycle-manager), Go 1.24+
    ├── Dockerfile                     distroless static nonroot, cross-compiles ($BUILDPLATFORM)
    ├── cmd/lifecycle-manager/main.go  wiring only
    ├── e2e/                           //go:build e2e — tiers 1–2 (needs minikube [+docker])
    └── internal/
        ├── config/                    HLM_* env parsing, *_FILE secrets
        ├── session/                   id rules, annotation vocabulary, derived-state machine
        ├── k8s/                       Client iface, dynamic impl, in-memory Fake
        │   └── unstructured.go        ⭐ ALL CRD field paths live here and nowhere else
        ├── connector/                 Client iface, HTTP impl (429 guard), Disabled noop
        ├── lifecycle/                 Manager: create/get/list/wake/decommission (shared core)
        ├── httpapi/                   net/http handlers (Go 1.22 method+path ServeMux)
        ├── sweeper/                   runner (jittered ticker), idle.DecideIdle (pure), TTL
        └── reconcile/                 claims↔instances diff report → /status
```

## 3. Decisions made (with the user), and why

| Decision | Choice | Why |
|---|---|---|
| Language | **Go** | design doc's first choice; k8s-native tooling; tiny static image |
| Backend | **Kubernetes-only** (agent-sandbox), local dev on **minikube** | user preference — no Docker driver abstraction; the existing docker-driver `minikube` profile is reused |
| k8s access | **dynamic client + unstructured**, no controller-runtime, no generated clientset | upstream is young (v1alpha1→v1beta1 churn already happened); our surface is ~6 field paths, isolated in one file; this is an HTTP service + poll loops, not a reconciler. GVR group/version are env-configurable |
| Relay integration | in scope, behind `HLM_CONNECTOR_ENABLED` | core stays generic without it |
| Orphan policy | **report-only** | auto-deleting connector state that might belong to non-orchestrator gateways is the wrong default |
| Repo/publishing | public library repo (LM + OCI chart); private instancing repo hermes-private-cluster | separation of concerns, 2026-08-11 |

## 4. Substrate facts (agent-sandbox v0.5.4) — the load-bearing surprises

Full detail + measured timings: **docs/p-m0.md**. The ones that bite:

1. **v1beta1 `SandboxClaim` requires `spec.warmPoolRef.name`** — NOT
   `sandboxTemplateRef` (that's v1alpha1). Claims reference a warm pool; the
   pool references the template. A `replicas: 0` pool is valid and cold-starts
   every claim from the pool's template. Hence config knob `HLM_WARM_POOL`,
   not a template name.
2. **Two API groups**: `Sandbox` is `agents.x-k8s.io`; claim/template/pool
   are `extensions.agents.x-k8s.io`. RBAC must name both (hack/e2e/rbac.yaml).
3. **Bound sandbox name comes from `claim.status.sandbox.name`** — differs
   from the claim name on warm-pool adoption. Never assume sandbox == claim.
4. Suspend = merge-patch `Sandbox.spec.operatingMode`. The claim controller
   never writes that field, so we don't fight it. Conditions: `Ready`,
   `Suspended` (+`Finished` for terminal pods).
5. PVCs are `<volumeClaimTemplate>-<sandboxName>`, owner-ref'd to the
   Sandbox ⇒ claim delete cascades pod+service+PVC (asynchronously — poll,
   don't assume immediate).
6. The SandboxClaim CRD has a **conversion webhook**; claims can't even be
   listed until the controller pod is Ready (minikube-up.sh waits for this).
7. `spec.lifecycle` on claims (shutdownTime/ttl) is **never used** — design
   §4.4: session destruction has exactly one code path, ours.

Measured on minikube (hostpath): cold start→Ready **2.4 s**, resume (PVC
reattach) **2.3 s**, suspend ~32 s only because busybox ignores SIGTERM
(30 s grace; e2e template sets `terminationGracePeriodSeconds: 2`).

## 5. The session model

- **Session id = claim name = connector gatewayId.** One DNS-1123 string
  (`^[a-z0-9]([a-z0-9-]{0,50}[a-z0-9])?$`), generated `s-<10 hex>` if absent.
- The LM is stateless. Its entire "database" is claim metadata:
  label `hermes.nabi.dev/managed=true` (sweep scope — unlabeled claims are
  invisible) + annotations `hermes.nabi.dev/{ttl-seconds, idle-timeout-seconds,
  connector, display-name}`. Restart rebuilds everything from claim list +
  connector instance list (verified by `TestTier1Statelessness`).
- **Derived state, never stored** (`session.Derive`): Terminating (deletion
  timestamp) → Pending (no sandbox) → Suspended/Suspending (mode=Suspended,
  by `Suspended` condition) → Ready/Waking (mode=Running, by `Ready`
  condition). Note: operatingMode wins over a stale Ready=True.

## 6. HTTP API (full reference: docs/api.md)

`POST/GET /v1/sessions`, `GET/DELETE /v1/sessions/{id}`,
`GET|POST /wake/{session}`, `GET /healthz|/readyz|/status`.
Errors are `{"error":"..."}`. Optional `HLM_API_TOKEN` bearer guards `/v1/*`
**only** — `/wake` must stay unauthenticated (the connector's poke is a bare
GET), and this is asserted by unit test.

Wake semantics (design §5.1): patch `operatingMode: Running` unconditionally,
idempotent, safe mid-suspension; 500 on API-server failure so the connector's
next cooldown-gated poke retries. A lost poke degrades to
delivered-on-next-resume, never loss (drain-on-reconnect needs no poke).

`POST /v1/sessions` is deliberately the seam a future pairing flow calls
(design §12 open question 1).

## 7. Sweepers — the only writers of lifecycle intent

One jittered ticker (`HLM_SWEEP_INTERVAL`, default 60 s; 30 s per-cycle
timeout; first sweep immediately at startup).

**Idle sweeper** (runs only with the connector; without it there is no
activity signal and we never guess). `sweeper.DecideIdle` is a pure function;
suspend requires ALL of: effective idleTimeout > 0 · state Ready ·
Ready-condition `lastTransitionTime` older than the timeout (protects a
freshly resumed pod whose gateway hasn't spoken yet) · instance exists and
not revoked · `turnInFlight == false` · **both** `lastInboundAt` and
`lastOutboundAt` non-null (they're in-memory on the connector; null after its
restart = unknown, and unknown is NOT idle) · newest activity older than the
timeout · `bufferedCount == 0` (buffered work is about to run — also avoids
racing the wake poke).

**TTL sweeper**: claim age ≥ effective TTL ⇒ `Manager.Decommission` — the
same single function `DELETE /v1/sessions/{id}` uses: connector
`DELETE /admin/v1/instances/{id}` first (purges buffer+routes+row; 404 =
already done), then claim delete (cascade). If deprovision fails the claim is
**kept** and the next sweep retries — claim existence is the retry state.

Per-session annotation overrides beat globals; `"0"` disables per session.

## 8. Connector integration — the contract and its traps

Client in `internal/connector` (typed, camelCase JSON, unix-second ints).
Traps that already bit us or were designed around:

1. **`routeKeys` on provision ≠ chat routes.** Provision's `routeKeys` feeds
   the per-gateway routing row (`routes` table). Chat→gateway bindings are a
   different table (`chat_routes`) managed ONLY via
   `POST/GET/DELETE /admin/v1/routes`. `Manager.Create` therefore provisions
   AND binds each chat explicitly, with full rollback (deprovision + claim
   delete → 502) if any step fails. Found by tier-2 e2e.
2. **Never retry.** The connector throttles 10 auth failures / source IP /
   60 s and then 429s *even good credentials*. Any 429 sets a shared
   `throttledUntil` (65 s) and all calls short-circuit with `ErrThrottled`;
   sweepers skip the cycle; the sweep cadence is the retry loop. There is no
   retry logic anywhere in the client — keep it that way.
3. **The provision response contains the gateway secret — we drop it.** The
   decode struct simply has no secret fields; the agent pod re-provisions on
   boot and rotates within the connector's 2-deep verify window (COALESCE
   semantics mean our wakeUrl survives that rotation). Unit test asserts no
   leak.
4. **No single-instance GET** — list and filter (cached ≤5 s in
   `Manager.Instances` so a session list costs one admin call).
5. **Admin listener has no `/healthz`** (when `HRC_ADMIN_LISTEN` splits
   planes). Reachability is probed via the **unauthenticated `GET /metrics`**
   (v0.2.0+), deliberately bypassing the throttle guard.
6. `PATCH /admin/v1/instances/{id}` can set `wakeUrl`/`displayName` only;
   nulls never clear values (`""` disables poking); `instanceId` is
   provision-time-only.
7. Wake poke mechanics: fired only on the **first** buffered event for a
   disconnected gateway, per-gateway cooldown (`HRC_WAKE_COOLDOWN_SECS`,
   default 60), 10 s timeout, no retries, no auth, no payload.

## 9. Configuration

See README table. Notables: `HLM_WARM_POOL` is required; `HLM_CONNECTOR_URL`
is the **admin-plane** base; `HLM_WAKE_BASE_URL` is what gets registered as
wakeUrl (`<base>/wake/<id>` — in-cluster this is the LM Service URL);
durations accept `30m` or bare seconds; every secret has a `_FILE` variant
that wins and fails loudly on empty/unreadable files. Kubeconfig fallback
(`KUBECONFIG` → `~/.kube/config`) makes `make run-local` work against
minikube.

## 10. Testing

- **Unit** (`make test`): derived-state matrix; `DecideIdle` guard-by-guard;
  connector client vs an httptest fake implementing the real contract
  (bearer auth, 429 window, `{"error":...}` bodies, `/metrics`); secret-leak
  assertion; handler tests incl. provision/route rollback, wake idempotency +
  auth exemption, deprovision-failure claim retention; sweeper integration
  over fakes; report-only orphan proof.
- **e2e** (`make e2e`, `//go:build e2e`, skips cleanly when prereqs missing):
  - Tier 0: `hack/p-m0/run.sh` — substrate suspend/resume/PVC/cascade loop.
  - Tier 1 (minikube, no connector): CRUD, wake, TTL sweep, LM "restart".
  - Tier 2 (+docker): real connector container, **agentless full wake loop**
    via its echo API — provision → suspend → echo message buffers → real wake
    poke → LM patches Running → Ready; deprovision purge; orphan report.
    Image candidates in order: `$HLM_E2E_CONNECTOR_IMAGE`, local `hrc:e2e`,
    GHCR `v0.2.0`. The connector container needs
    `--add-host host.docker.internal:host-gateway` (Linux) and the LM must
    listen on 0.0.0.0 so the poke can reach the host. Do NOT override
    `HRC_DB` — the image is `FROM scratch`; `/data` is its only writable path.
- All green as of handover: 5 e2e tests + full unit suite.

## 11. CI / release

`.github/workflows/ci.yml`: vet+lint+unit (race) → multi-arch image →
GHCR `ghcr.io/nabi-allenby/hermes-cluster/lifecycle-manager` (`:sha` on main,
semver on `v*` tags) → minikube e2e job (tier 0 + tiers 1–2; tier 2 skips in
CI until a connector image is pullable there). The Dockerfile cross-compiles
from `$BUILDPLATFORM` (`GOOS`/`GOARCH` from buildx args), so arm64 never runs
under QEMU; the image job uses `type=gha` layer cache.

The connector image is public on GHCR as
`ghcr.io/nabi-allenby/hermes-relay-connector:0.2.0` (bare semver — the
release workflow strips the `v`; tags: 0.1.0, 0.2.0, latest; anonymous
pull verified 2026-08-10). Tier-2 e2e falls back to it automatically when
no local `hrc:e2e` exists, and CI's e2e job logs in to GHCR with
`GITHUB_TOKEN`, so tier 2 runs in CI too.

## 12. Current status & next steps

**Done** (maps to design §10): P-M0 ✅ (substrate validated + timings);
P-M2 core ✅ on minikube — P-AC1/2/3's k8s halves plus the agentless wake
loop are proven. **P-M1 ✅** — the real hermes-agent (@244d296,
`hermes-agent:local`) runs as a sandbox session: PVC home, first-boot seed
from Secret, boot-time self-provision, connected-idle in 5.7 s from claim
create, graceful suspend (~4 s), re-auth from persisted state on resume,
buffered backlog drains. Recipe + timings in docs/p-m1.md; drill
`hack/p-m1/run.sh` (`make p-m1`). Substrate surprise found: sandbox pods
default to `dnsPolicy: None` with public resolvers — templates must set
`ClusterFirst` explicitly (recorded in docs/p-m0.md).

**P-AC1 ✅ (live, 2026-08-11, `hack/p-ac1/`)** — full loop on minikube with
the in-cluster LM (its first in-cluster run) + connector fronting the real
Discord bot (Apollo): DM → conversational LLM reply; ~2.5 min silence → idle
sweeper suspends (pod gone, disk-only); DM while suspended → buffer → wake
poke → LM resumes → agent drains and replies. Decommission via
`DELETE /v1/sessions` verified live: deprovision + full cascade in 6 s,
connector left with zero instances. Notes: the LM ran in-cluster with RBAC
from hack/e2e/rbac.yaml unchanged; `HRC_DEFAULT_GATEWAY` unset routes DMs to
the single session; the seed home's OpenRouter key had expired and was
refreshed (dead key symptom: agent replies "Provider authentication failed",
OpenRouter returns 401 "User not found"). `hack/p-ac1/down.sh` tears the
demo down; the old Docker parity demo containers (`hrc`, `hermes-agent-1`)
were `docker stop`ed to avoid double-fronting the bot — `docker start` them
to restore.

**P-M3 authored ✅ (2026-08-11)** — `charts/hermes-platform` (connector
`Recreate`×1 + buffer PVC fsGroup 65532, LM with restricted securityContext,
generated-and-kept admin/provision tokens via `lookup`, the P-M1 session
template with `dnsPolicy: ClusterFirst`, NetworkPolicies default-on,
session nodeSelector/tolerations for the tainted Spot pool). Lifecycle
drill green against the chart release on minikube (wake <2 s via echo
buffer, run from an in-cluster pod — port-forwards are too flaky for
2-minute waits). Terraform: `modules/aks` (Free tier, single-AZ amd64 Spot
D2as_v5, Azure CNI + netpol, workload identity, Key Vault),
`modules/platform` (agent-sandbox manifest via kubectl provider, secrets,
helm_release), `examples/aks-personal` — validate-clean (OpenTofu; brew has
no modern terraform post-BSL). Live minikube runs the chart in ns `hermes`
(release `hermes`, dev timings: cooldown 5 s, sweep 15 s).

**P-AC4 ✅ (live on AKS, 2026-08-11 — full record: docs/p-ac4.md)** —
terraform (aks-personal, sandbox subscription) + chart → Discord
conversation from an AKS pod, idle suspend (8 s), wake-on-message
(message→connected ~22 s; **managed-csi attach ≈ 15 s** — §5.2's last
unknown, measured). Azure CNI enforcement caught a real netpol bug
(sandbox pods are labeled `sandbox-name-hash`, fixed). Deviations logged in
docs/p-ac4.md: Regular priority (westeurope Spot drought), images from tmp
ACR `hermestmp10502` (GHCR packages still private), hlm image/SA drift on
the live release, chart from repo path (OCI publish pending). **The AKS RG
`hermes-cluster` + `hermes-tmp` (ACR) are LIVE and billing ~$3.7/day until
destroyed.**

**Library split ✅ (2026-08-11)** — this repo is now the public-intent
library: lifecycle-manager (GHCR semver images; v0.1.0 tagged) + the chart
as OCI (`chart-v0.1.0` published to
`oci://ghcr.io/nabi-allenby/hermes-cluster/charts/hermes-platform`).
Agent image: official `nousresearch/hermes-agent` (Docker Hub, multi-arch;
pin release tags — conformance pin @244d296 ≈ v2026.8.3). ALL terraform
moved to the private instancing repo
**github.com/nabi-allenby/hermes-private-cluster** together with the LIVE
tofu state (28 resources — the running AKS deployment is managed from
there now). Git history here scanned clean for secrets (22 commits).

Repo governance (2026-08-11): both repos are **private**. Branch
protection rules exist on this repo's `main` (no force-push/deletion, CI
`test` required) but are **dormant** — GitHub Free doesn't enforce
protection on private repos; they reactivate if the repo goes public.
hermes-private-cluster: private, unprotected, accepted. The repo was
briefly public on 2026-08-11 and reverted the same day; the "public
open-source library" step is deferred until the user chooses to flip it.

**Not done:**

1. **Org-admin visibility clicks**: GHCR packages `lifecycle-manager` +
   `charts/hermes-platform` → public (`hermes-agent` GHCR package is
   obsolete — deletable).
2. **Reconcile live AKS drift** via the instancing repo (next apply moves
   hlm to the GHCR 0.1.0 image + official agent image, then drop the tmp
   ACR `hermes-tmp` RG and the ACR_* repo secrets here).
3. **P-M4 — survivability drill** (Spot eviction ≡ involuntary suspend;
   needs a region/time with Spot capacity), EKS example + weekly CI, and a
   `lifecycleManager.imagePullSecrets` chart value for private-registry
   setups.
3. **Design's one remaining measurement**: Azure managed-csi attach latency
   (the wake budget's only unknown; minikube can't answer it).
4. Nice-to-haves queued: idle-sweep e2e with a stub WS gateway
   (`hack/stubgw/` idea) if echo-only can't move `turnInFlight`; LM
   Prometheus metrics; `HLM_ORPHAN_POLICY=deprovision` opt-in; a small
   operator CLI wrapping the LM API (create/list/decommission).

## 13. Operational notes

- gh CLI authed as **ChefControl** (member of nabi-allenby). Git identity:
  ChefControl. Commits so far end with a Claude co-author trailer.
- minikube: existing `minikube` profile (docker driver) is reused by
  `hack/minikube-up.sh`; agent-sandbox v0.5.4 is installed in
  `agent-sandbox-system`.
- The sibling checkout `~/Downloads/hermod` is the connector repo (its
  `main` carries v0.2.0). Tier-2 uses `docker build -t hrc:e2e ~/Downloads/hermod`.
- Pin-bump procedure: agent-sandbox → re-verify every fact in docs/p-m0.md
  (rerun `make p-m0`), all field paths are in `internal/k8s/unstructured.go`;
  connector → re-read its `src/http_api.rs` against `internal/connector` and
  rerun tier 2.
