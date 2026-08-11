# charts/hermes-platform

The platform as a Helm chart: connector Deployment + buffer PVC (1 replica,
`Recreate` — SQLite), lifecycle-manager Deployment + RBAC, the P-M1 session
`SandboxTemplate`/`SandboxWarmPool`, NetworkPolicies. Cloud-agnostic:
storage classes, image refs, and scheduling (nodeSelector/tolerations) are
values. Validated end-to-end on minikube (create → connected → suspend →
echo-buffer → wake <2 s → drain → decommission cascade).

## Prerequisites (the chart references, never creates)

1. **agent-sandbox CRDs + controller** (pinned v0.5.4) — install the
   upstream release manifest (`hack/minikube-up.sh` does it for minikube;
   see docs/agent-sandbox.md for the manifest URL).
2. **Secret `hermes-home-seed`** — first-boot `HERMES_HOME`: keys `.env`
   (LLM key), `auth.json`, `config.yaml`, `SOUL.md`. Build locally with
   `hack/drills/1-agent-session/make-seed.sh`.
3. **Secret `hrc-discord-token`** (key `token`) — optional; without it the
   connector runs Discord-less (echo/relay only).
4. **The agent image** (`session.image`) — defaults to the official
   multi-arch `nousresearch/hermes-agent:v2026.8.3` (Docker Hub). Pin a
   release tag or digest, never `latest`; the relay contract was
   conformance-verified around commit `244d296`.

Admin/provision tokens are generated on first install and kept across
upgrades (`connector.existingTokensSecret` to bring your own).

## Install

```bash
helm install hermes charts/hermes-platform -n hermes --create-namespace
```

Dev-loop timings: `--set connector.wakeCooldownSeconds=5 --set lifecycleManager.sweepInterval=15s`.

## Notes

- `session.warmReplicas` must stay 0: the session identity chain
  (gatewayId == pod == claim name) only holds on cold starts; warm-pool
  adoption breaks it (design §12 open question).
- NetworkPolicies are default-on and need an enforcing CNI (AKS Azure CNI
  does; plain minikube's kindnet does not — inert there).
- The `dnsPolicy: ClusterFirst` in the template is load-bearing:
  agent-sandbox v0.5.4 otherwise renders sandbox pods with public resolvers
  and cluster Services never resolve (docs/agent-sandbox.md).
