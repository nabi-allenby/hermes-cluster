# terraform/

Invariants (design §9): Terraform owns everything with a cloud ID; Helm owns
everything with an apiVersion; TF outputs feed values, never the reverse.

- **`modules/platform`** — cloud-agnostic: agent-sandbox release manifest
  (pinned v0.5.4, server-side apply), namespace, the two out-of-band secrets
  (Discord token, HERMES_HOME seed), `helm_release` of the chart.
- **`modules/aks`** — RG, VNet, AKS **Free tier**, single-AZ **amd64 Spot**
  pool (D2as_v5, `Delete` eviction — eviction ≡ involuntary suspend), Azure
  CNI with NetworkPolicy enforcement, workload identity, Key Vault. The
  ~$25–36/mo standing deployment (design §8).
- **`examples/aks-personal`** — wires both; `terraform validate` clean.
  EKS module + weekly CI: P-M4.

## aks-personal bring-up

```bash
cd terraform/examples/aks-personal
terraform init
# subscription + tenant come from your current az login context:
export ARM_SUBSCRIPTION_ID=$(az account show --query id -o tsv)
terraform apply \
  -var discord_token="$(cat ~/.config/hrc/discord.token)" \
  -var seed_dir=~/my-hermes-seed
```

`seed_dir` holds `.env` / `auth.json` / `config.yaml` / `SOUL.md`.

**The agent image is your one build step**: no official hermes-agent image
exists, and the Spot pool is amd64 — build from
NousResearch/hermes-agent@244d296 (`docker buildx build --platform
linux/amd64`) and push somewhere the cluster can pull. Everything else is
`terraform apply`.

Works with OpenTofu (`tofu`) identically — Homebrew ships tofu; genuine
Terraform is `brew install hashicorp/tap/terraform`.

Sessions tolerate the Spot taint and pin to the `agents` pool via chart
values (see the example's `values_yaml`); connector + LM stay on the system
pool. First real apply should also record the design's last open
measurement: managed-csi attach latency on resume (design §5.2).
