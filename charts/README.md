# charts/ — placeholder

`charts/hermes-platform` lands in milestone P-M3 (design §9): connector
Deployment + buffer PVC (1 replica, `strategy: Recreate` — SQLite), the
lifecycle-manager Deployment, RBAC (see `hack/e2e/rbac.yaml` for the exact
rules), NetworkPolicies, SandboxTemplates, and the warm pool. Cloud-agnostic:
StorageClass, runtime class, and identity annotations are values.
