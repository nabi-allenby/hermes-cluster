# charts/hermes-platform

The platform as a Helm chart: connector Deployment + buffer PVC (1 replica,
`Recreate` — SQLite), lifecycle-manager Deployment + RBAC, the P-M1 session
`SandboxTemplate`/`SandboxWarmPool`, NetworkPolicies. Cloud-agnostic:
storage classes, image refs, and scheduling (nodeSelector/tolerations) are
values. Validated end-to-end on minikube (create → connected → suspend →
echo-buffer → wake <2 s → drain → decommission cascade).

## Prerequisites (the chart references, never creates)

1. **agent-sandbox CRDs + controller** (pinned v0.5.4) — installed by the
   terraform `platform` module or `hack/minikube-up.sh`.
2. **Secret `hermes-home-seed`** — first-boot `HERMES_HOME`: keys `.env`
   (LLM key), `auth.json`, `config.yaml`, `SOUL.md`. Build locally with
   `hack/p-m1/make-seed.sh`.
3. **Secret `hrc-discord-token`** (key `token`) — optional; without it the
   connector runs Discord-less (echo/relay only).
4. **A pullable hermes-agent image** matching your node architecture
   (`session.image`). There is no official registry image; the conformance
   pin is NousResearch/hermes-agent `@244d296`. On minikube:
   `minikube image load hermes-agent:local`. On AKS (amd64 Spot pool): build
   amd64 and push to a registry the cluster can reach.

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
  and cluster Services never resolve (docs/p-m0.md).
