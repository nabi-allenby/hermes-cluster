# Testing

Everything runs locally against **minikube** — no cloud account needed.
The ladder, cheapest first:

| Rung | Command | Needs | Verifies |
|---|---|---|---|
| Unit | `make test` | Go only | state machine, sweepers, connector client vs a contract-faithful fake, handlers |
| e2e tier 0 | `make drill-substrate` | minikube | agent-sandbox substrate: claim → suspend → PVC survives → resume |
| e2e tiers 1–2 | `make e2e` | minikube (+docker) | session CRUD/wake/TTL/statelessness; tier 2 adds a real connector container and the agentless wake loop |
| Agent drill | `make drill-agent` | + agent image, seed home | the real hermes-agent: boot → self-provision → connect → suspend → buffered wake → drain |
| Live drill | `make drill-live` | + Discord bot token | the chart fronting your bot: converse, auto-suspend, wake by DM |

## Environment

```bash
make minikube-up    # test/env/minikube-up.sh — minikube + pinned agent-sandbox
```

Reuses an existing `minikube` profile (docker driver). The agent-sandbox
version is pinned in the script (`AGENT_SANDBOX_VERSION`).

## Layout

- `env/` — local environment bootstrap.
- `fixtures/` — `template.yaml` (busybox template/pool, fast suspend) is
  applied by the Go e2e suite by path; `rbac.yaml` is the lifecycle-manager's
  exact RBAC as a reference artifact, mirrored by the chart.
- `drills/` — script-driven scenarios against real components, numbered in
  dependency order. Each `run.sh` is idempotent and prints a timing table
  or next-step instructions; `2-live-discord/down.sh` tears down.

## Drill prerequisites

- **`drills/1-agent-session`**: images tagged exactly `hermes-agent:local`
  and `relay-connector:e2e` in the local docker daemon (the drill loads them into
  minikube; the GHCR connector fallback applies to `make e2e`, not here), and a seed
  home directory — `make-seed.sh <dir>` turns `.env` (LLM key),
  `auth.json`, `config.yaml`, `SOUL.md` into the cluster Secrets. Nothing
  secret is ever committed.
- **`drills/2-live-discord`**: `DRILL_SEED_DIR` (same seed home), a Discord
  bot token file (`DRILL_DISCORD_TOKEN_FILE`, default
  `~/.config/hrc/discord.token`) and `DRILL_BOT_ID` (your bot's application id). Uses the chart itself with
  fast dev timings, so it doubles as chart validation.

## Conventions

- e2e tests skip cleanly when their prerequisites are missing — a plain
  `go test -tags e2e` on a laptop without minikube must not fail.
- Drills never store secret values in the repo or print them.
- The connector is never retried on auth errors (it throttles per source
  IP); drills treat the sweep cadence as the retry loop, same as the code.
