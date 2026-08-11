# Contributing

## Setup

Go 1.24+, docker, minikube, helm. Then:

```bash
make minikube-up   # local test environment (minikube + pinned agent-sandbox)
make test          # unit suite — fast, no cluster
```

## Before opening a PR

1. `make test` and `make vet` pass.
2. If you touched lifecycle logic or the connector client: `make e2e`
   (tiers 1–2 against minikube; tier 2 skips without docker).
3. If you touched the chart: `make chart-lint`, and ideally
   `make drill-live` against your own bot.
4. If you bumped a pin, follow its upgrade rule in
   [docs/architecture.md](docs/architecture.md#pins-and-upgrade-rules) —
   agent-sandbox bumps re-verify [docs/agent-sandbox.md](docs/agent-sandbox.md).

The full testing ladder and drill prerequisites: [test/README.md](test/README.md).

## Design ground rules

- The lifecycle-manager stays **stateless** — claim metadata is the only
  store; state is derived, never persisted.
- Session destruction has **exactly one code path** (`Manager.Decommission`).
- **No retries** in the connector client — the sweep cadence is the retry
  loop (the connector throttles auth failures aggressively).
- All agent-sandbox CRD field paths live in
  `lifecycle-manager/internal/k8s/unstructured.go` and nowhere else.

Full design: [docs/architecture.md](docs/architecture.md).
