# P-AC4 — live run record (AKS, 2026-08-11)

`terraform apply` (aks-personal) + two out-of-band secrets + the in-repo
chart produced a working platform on a fresh subscription; P-AC1/2/3 were
then verified live against it. Bot conversed on Discord from an AKS pod;
idle → suspend to disk-only; DM while suspended → buffered → wake poke →
resume on the same PVC → reply. Decommission path unchanged from minikube.

## The measurement the design was waiting for (§5.2)

Wake, from the connector's poke:

| Step | Measured |
|---|---|
| poke → `operatingMode: Running` patched | < 1 s |
| pod scheduled + **managed-csi attach** + containers started | **17 s** |
| gateway re-provision + relay reconnect | **+5 s** |
| **message → connected total** | **~22 s** |

minikube hostpath resumed in ~2 s, so **managed-csi attach ≈ 15 s** of the
17. Budget: ≤60 s hard / ≤30 s target — met with ~8 s headroom before the
LLM turn. Suspend (sweep decision → `Suspended=True`, pod gone): **8 s**.
First cold boot: ACR pull 31 s (980 MB compressed, intra-region) + seed +
self-provision.

## What enforcement taught us (fixes already committed)

- **Azure CNI enforces NetworkPolicy; minikube's kindnet never did.** The
  session selector had to change to the label pods actually carry:
  `agents.x-k8s.io/sandbox-name-hash` (not `sandbox-name`). The policies
  otherwise held: platform paths (relay dial-out, wake poke, LLM egress)
  worked; out-of-band drill traffic was correctly blocked (use
  `kubectl port-forward`, which NetworkPolicy does not gate).
- SKU roulette is real: subscription allow-lists (D2as_v5 rejected),
  then Spot capacity droughts (D2as_v6/v7, D2s_v6 all dry in westeurope).
  The module now defaults non-zonal, Intel `D2s_v6`, with
  `agent_pool_priority` as the escape hatch.

## Honest deviations from the design in this run

1. **Agent pool ran `Regular`, not Spot** — westeurope had no 2-vCPU Spot
   capacity that day, on either vendor line. Spot remains the default;
   P-M4's eviction drill needs a region/time with capacity.
2. **Images came from a throwaway ACR** (`hermestmp10502`, admin creds) —
   the GHCR agent/LM packages were private and the visibility flip needs an
   org admin. Standing path: make
   `ghcr.io/nabi-allenby/hermes-cluster/{hermes-agent,lifecycle-manager}`
   public, publish the chart as OCI, drop the ACR.
3. **Two pieces of live drift** on the AKS release, pending chart/values
   parity: `deploy/hermes-hlm` image was `kubectl set image`'d to the ACR
   build (the `:dev` GHCR tag doesn't exist), and its ServiceAccount was
   patched with `imagePullSecrets: [acr-pull]`. A future
   `lifecycleManager.imagePullSecrets` value should absorb both.
4. Chart came from the repo path, not OCI (publish pending).

## Cost while up

Regular D2s_v6 agents node + B2s system + Free control plane + disks ≈
$3.5/day; tmp ACR Basic ≈ $0.17/day. `tofu destroy` + delete RG
`hermes-tmp` returns to zero (GHCR artifacts persist, free).
