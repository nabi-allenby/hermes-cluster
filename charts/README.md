# charts/hermes-cluster

The platform as a Helm chart: connector Deployment + buffer PVC (1 replica,
`Recreate` — SQLite), lifecycle-manager Deployment + RBAC, the session
`SandboxTemplate`/`SandboxWarmPool`, NetworkPolicies. Cloud-agnostic:
storage classes, image refs, and scheduling (nodeSelector/tolerations) are
values. Validated end-to-end on minikube (create → connected → suspend →
echo-buffer → wake → drain → decommission cascade; measured timings in
[../docs/architecture.md](../docs/architecture.md)).

## Prerequisites (the chart references, never creates)

1. **agent-sandbox CRDs + controller** (pinned v0.5.4) — install the
   upstream release manifest (`test/env/minikube-up.sh` does it for minikube;
   see [../docs/agent-sandbox.md](../docs/agent-sandbox.md) for the manifest URL).
2. **Secret `hermes-home-seed`** — first-boot `HERMES_HOME`: keys `.env`
   (LLM key), `auth.json`, `config.yaml`, `SOUL.md`. Build locally with
   `test/drills/1-agent-session/make-seed.sh`.
3. **Secret `relay-connector-discord-token`** (key `token`) — optional; without it the
   connector runs Discord-less (echo/relay only).
4. **The agent image** (`session.image`) — defaults to the official
   multi-arch `nousresearch/hermes-agent:v2026.8.3` (Docker Hub). Pin a
   release tag or digest, never `latest`; the relay contract was
   conformance-verified around commit `244d296`.

Admin/provision tokens are generated on first install and kept across
upgrades (`connector.existingTokensSecret` to bring your own).

## Install

```bash
helm install hermes oci://ghcr.io/nabi-allenby/hermes-cluster/charts/hermes-cluster \
  --version 0.2.2 -n hermes --create-namespace
```

From a checkout: `helm install hermes charts/hermes-cluster ...`.

Dev-loop timings: `--set connector.wakeCooldownSeconds=5 --set lifecycleManager.sweepInterval=15s`.

## Security notes

- **Sessions mutually trust each other's provisioning.** The pre-shared
  provision token is mounted into every session pod (self-provision needs
  it) and can rotate *any* existing gatewayId — a compromised agent pod
  could hijack a sibling session's relay identity. Fine single-tenant; do
  not host mutually-distrusting users until
  [hermes-relay-connector#30](https://github.com/nabi-allenby/hermes-relay-connector/issues/30)
  lands.
- Generated tokens use Helm's crypto-grade `randAlphaNum` and are reused
  across upgrades via `lookup` — which only works with server-side renders.
  Client-side `helm template` GitOps flows (e.g. ArgoCD default) would
  re-mint them every sync: set `connector.existingTokensSecret` there.

## Notes

- `session.warmReplicas` must stay 0: the session identity chain
  (gatewayId == pod == claim name) only holds on cold starts; warm-pool
  adoption breaks it (open question).
- NetworkPolicies are default-on and need an enforcing CNI (AKS Azure CNI
  does; plain minikube's kindnet does not — inert there).
- The `dnsPolicy: ClusterFirst` in the template is load-bearing:
  agent-sandbox v0.5.4 otherwise renders sandbox pods with public resolvers
  and cluster Services never resolve ([../docs/agent-sandbox.md](../docs/agent-sandbox.md)).
