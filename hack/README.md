# hack/

Minikube bootstrap, e2e fixtures, and the reproducible drills — in the
order you'd run them.

| Path | What | Needs |
|---|---|---|
| `minikube-up.sh` | start minikube + install pinned agent-sandbox | docker, minikube |
| `e2e/` | fixtures the Go e2e suite applies by path (busybox template/pool, exact LM RBAC) | — |
| `drills/0-substrate/` | prove the agent-sandbox substrate: claim → suspend → PVC survives → resume (`make p-m0`) | minikube |
| `drills/1-agent-session/` | the real hermes-agent as a session: boot → self-provision → connect → suspend → buffered wake → drain (`make p-m1`) | + agent image, connector image, seed home (`make-seed.sh`) |
| `drills/2-live-discord/` | the chart fronting your real Discord bot; converse, watch auto-suspend, wake by DM | + Discord bot token, `PAC1_BOT_ID` |

Drill history and measured timings live in `docs/` (p-m0, p-m1, p-ac4 —
named for the design's milestones; the directories here are named for what
they do).

Seed home: `drills/1-agent-session/make-seed.sh <dir>` builds the Secrets
from a directory containing `.env` (LLM key), `auth.json`, `config.yaml`,
`SOUL.md`. Nothing secret is ever committed; the boot-time self-provision
script (`bootstrap.py`) has one canonical copy in the chart.
