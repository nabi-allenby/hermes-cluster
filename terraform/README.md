# terraform/ — placeholder

Lands in milestones P-M3/P-M4 (design §9):

- `modules/platform` — cloud-agnostic: agent-sandbox release manifest,
  namespace, secrets wiring, `helm_release` of the chart.
- `modules/aks` — RG/VNet/AKS Free tier/Spot pool/Workload Identity/Key Vault
  (the ~$25–36/mo standing deployment).
- `modules/eks` — adopter path, validated by a weekly apply/destroy CI job.
- `examples/{aks-personal, eks-full, byo-cluster}`.

Invariants: Terraform owns everything with a cloud ID; Helm owns everything
with an apiVersion; TF outputs feed values, never the reverse.
