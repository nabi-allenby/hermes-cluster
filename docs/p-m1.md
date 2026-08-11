# P-M1 — headless Hermes pod recipe on Kubernetes

The real [NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent)
(conformance pin **@244d296**, image `hermes-agent:local`) running as an
agent-sandbox session: PVC-backed `HERMES_HOME`, non-interactive boot to
connected-idle against an in-cluster dev connector, and re-auth from persisted
state across suspend/resume. Everything lives in `hack/drills/1-agent-session/`; the drill is
`hack/drills/1-agent-session/run.sh` (`make p-m1`).

Design ref: design §10 P-M1. Acceptance ("Restart → re-auth from persisted
state") is asserted by the drill's suspend → buffer → resume → drain leg.

## The recipe

One `SandboxTemplate` (`hermes-session`) + a `replicas: 0` `SandboxWarmPool`
(`hermes-pool`). Three mechanisms:

1. **Home on the PVC.** `volumeClaimTemplates` mounts `home` at `/opt/data`
   (the image's `HERMES_HOME`). The image's s6 stage2 hook chowns the
   top-level dir and its own subdirs to the baked-in `hermes` user
   (**uid/gid 10000**) — but *not* top-level files, which is why seeding must
   set ownership itself (below).
2. **First-boot seed.** An init container copies the four files a headless
   agent needs — `.env` (LLM keys etc., minus `GATEWAY_RELAY_*`),
   `auth.json`, `config.yaml`, `SOUL.md` — from Secret `hermes-home-seed`
   onto the PVC, `chown 10000:10000`, **only when absent**. A resumed or
   restarted session never has its state overwritten. `make-seed.sh` builds
   the Secret from a seed directory (default: the Docker-proven
   `~/Downloads/.hermes-container-home`).
3. **Self-provision on every boot.** The container args run
   `bootstrap.py` (ConfigMap-mounted) before `exec hermes gateway run
   --replace`: POST `/relay/provision` with the pre-shared token (Secret
   `hermes-provision-token`), then rewrite only the `GATEWAY_RELAY_*` lines
   of `$HERMES_HOME/.env`. Each boot rotates the gateway secret inside the
   connector's 2-deep verify window; provision COALESCE semantics preserve a
   lifecycle-manager-registered `wakeUrl`. `--replace` takes over a stale
   `gateway.lock` left by a hard suspend.

**Identity:** gatewayId == instanceId == session id == pod name (downward
API). On cold start pod == sandbox == claim name, which the drill asserts.
Warm-pool adoption breaks that equality — this template is for `replicas: 0`
pools only (same restriction the LM's session model already implies).

## Substrate/image facts found here

- **Sandbox pods get `dnsPolicy: None` + public resolvers by default** —
  agent-sandbox v0.5.4 injects `8.8.8.8`/`1.1.1.1` when the template is
  silent, so cluster Services don't resolve. Explicit
  `dnsPolicy: ClusterFirst` survives. (Also recorded in docs/p-m0.md.)
- The image's arg routing (`main-wrapper.sh`): a `sh -c ...` arg vector runs
  as the hermes user with the venv active and `HOME=/opt/data` — no wrapper
  image needed for the bootstrap.
- k8s runs the entrypoint as PID 1, so the full s6 supervision tree is live
  (same path as plain `docker run`).
- SIGTERM → the gateway sends `going_idle` and exits promptly: suspend took
  ~4 s against a 30 s grace period (busybox took the full 30 s in P-M0).
- The dev connector runs in-cluster from the same image e2e uses
  (`hrc:e2e`), `fsGroup: 65532` for `/data`, readiness probed via the
  unauthenticated `/metrics`.

## Measured (minikube, docker driver, hostpath; 2026-08-10)

| Step | Measured |
|---|---|
| claim create → pod Ready (cold, image cached) | **2.8 s** |
| pod Ready → gateway connected (seed + self-provision + WS handshake) | **2.7 s** (total claim → connected **5.7 s**) |
| suspend: patch → `Suspended=True` (graceful `going_idle`) | **3.9 s** |
| resume: patch → pod Ready | **1.7 s** |
| pod Ready → reconnected (re-provision + re-auth from persisted state) | **2.2 s** |
| reconnect → buffered backlog drained | ~30 s (ack-gated replay incl. agent turn processing) |

The design's ≤30 s wake budget: **patch → connected ≈ 3.9 s** on this
substrate — the agent's own boot adds ~2 s over the P-M0 busybox floor.
Backlog drain lands after wake and is not part of the budget (delivery, not
availability).

The echo round-trip reached the real agent (it answered with its relay
home-channel notice — inbound delivery, agent turn, outbound send all
exercised). A conversational LLM reply additionally needs valid credentials
in the seeded `.env`/`auth.json` and, for Discord, a bound home channel —
that is the P-AC1 leg, not P-M1.

## Not covered here (deliberately)

- No lifecycle-manager involvement: the drill patches `operatingMode`
  directly. The LM plugs in unchanged — point `HLM_WARM_POOL=hermes-pool`
  at this template's pool and its wake/suspend paths drive the same fields.
- No real Discord: the connector runs tokenless with the echo API. P-AC1
  (converse via Discord) adds `HRC_DISCORD_TOKEN` + a chat route/home
  channel on top of exactly this recipe.
- Image distribution: `hermes-agent:local` is loaded into minikube by the
  drill (`minikube image load`, ~4 GB, one-time). A registry-hosted agent
  image is a P-M3/P-M4 concern.
