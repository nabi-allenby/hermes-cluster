#!/usr/bin/env bash
# Live-Discord drill: deploy the CHART on minikube fronting your real Discord
# bot, create a session through the lifecycle-manager, then converse:
#
#   DM the bot -> reply -> go idle -> auto-suspend -> DM again -> wake -> reply
#
# Prereqs: the agent-session drill's prereqs (see ../1-agent-session/run.sh),
# plus a Discord bot token file (default ~/.config/hrc/discord.token) and
# your bot's application id in PAC1_BOT_ID.
# Leaves everything RUNNING; tear down with ./down.sh
set -euo pipefail
cd "$(dirname "$0")"

ns="${PAC1_NAMESPACE:-hermes}"
session="${PAC1_SESSION:-s-live-1}"
bot_id="${PAC1_BOT_ID:?set PAC1_BOT_ID to your Discord application/bot id}"
token_file="${PAC1_DISCORD_TOKEN_FILE:-$HOME/.config/hrc/discord.token}"
chart="../../../charts/hermes-cluster"
k() { kubectl -n "$ns" "$@"; }

echo ">> prereqs"
kubectl get crd sandboxclaims.extensions.agents.x-k8s.io >/dev/null \
  || { echo "FAIL: agent-sandbox not installed (test/env/minikube-up.sh)" >&2; exit 1; }
[ -s "$token_file" ] || { echo "FAIL: $token_file missing" >&2; exit 1; }

echo ">> secrets (seed reused from the agent-session drill tooling)"
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
PM1_NAMESPACE="$ns" ../1-agent-session/make-seed.sh >/dev/null
k create secret generic hrc-discord-token --from-file=token="$token_file" \
  --dry-run=client -o yaml | k apply -f - >/dev/null

echo ">> helm install/upgrade (dev timings)"
helm upgrade --install hermes "$chart" -n "$ns" \
  --set session.botId="$bot_id" \
  --set connector.wakeCooldownSeconds=5 \
  --set lifecycleManager.sweepInterval=15s >/dev/null
k rollout status deploy/hermes-hrc deploy/hermes-hlm --timeout=180s >/dev/null

echo ">> discord shard"
pf_pid=""
trap '[ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true' EXIT
shard=""
for _ in $(seq 1 30); do
  if [ -z "$pf_pid" ] || ! kill -0 "$pf_pid" 2>/dev/null; then
    k port-forward svc/hermes-hrc 18420:8420 >/dev/null 2>&1 &
    pf_pid=$!
  fi
  shard="$(curl -sf http://127.0.0.1:18420/metrics 2>/dev/null | grep '^hrc_discord_gateway_up' | awk '{print $2}' || true)"
  [ "$shard" = "1" ] && break
  sleep 1
done
[ "$shard" = "1" ] || { echo "FAIL: hrc_discord_gateway_up=$shard (bad token?)" >&2; exit 1; }
echo "   Discord shard READY"

echo ">> create session $session through the LM"
k port-forward svc/hermes-hlm 18080:8080 >/dev/null 2>&1 &
lm_pf=$!
trap '[ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null; kill "$lm_pf" 2>/dev/null || true' EXIT
for _ in $(seq 1 15); do curl -sf --max-time 2 http://127.0.0.1:18080/healthz >/dev/null 2>&1 && break; sleep 1; done
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:18080/v1/sessions \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"$session\",\"idleTimeoutSeconds\":120,\"ttlSeconds\":0,\"connector\":{}}")"
[ "$code" = "201" ] || [ "$code" = "409" ] || { echo "FAIL: create returned $code" >&2; exit 1; }

echo ">> wait for session Ready + gateway connected"
state=""
for _ in $(seq 1 120); do
  state="$(curl -sf "http://127.0.0.1:18080/v1/sessions/$session" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("state",""), d.get("connector",{}).get("connected",""))' 2>/dev/null || true)"
  [ "$state" = "Ready True" ] && break
  sleep 2
done
[ "$state" = "Ready True" ] || { echo "FAIL: session state/connected = '$state'" >&2; exit 1; }

cat <<EOF

READY — machine side done. Now the human side:

  1. DM the Discord bot: expect a reply from the agent.
  2. Stay quiet ~2-3 min (idle 120s + sweep 15s): the session suspends.
     Watch: kubectl -n $ns get sandbox $session -w
  3. DM again while suspended: buffer -> wake poke -> resume -> reply.

  LM logs:   kubectl -n $ns logs deploy/hermes-hlm -f
  Teardown:  ./down.sh
EOF
