#!/usr/bin/env bash
# P-AC1 setup: everything up to the point where a human DMs the Discord bot.
#
#   in-cluster connector (real Discord token) + in-cluster lifecycle-manager
#   -> session created THROUGH the LM (provision + wakeUrl registration)
#   -> agent pod boots, self-provisions, relay connects, Discord shard up
#
# Then: DM the bot. Expected loop: reply -> idle (120 s) -> LM suspends ->
# DM again -> buffer -> wake poke -> LM resumes -> reply.
#
# Prereqs beyond hack/p-m1's: ~/.config/hrc/discord.token, and the LM image
# (`make image VERSION=dev`, loaded by this script).
# This leaves everything RUNNING. Tear down with: hack/p-ac1/down.sh
set -euo pipefail
cd "$(dirname "$0")"

ns="${PAC1_NAMESPACE:-default}"
session="${PAC1_SESSION:-s-live-1}"
k() { kubectl -n "$ns" "$@"; }

echo ">> prereqs"
kubectl get crd sandboxclaims.extensions.agents.x-k8s.io >/dev/null \
  || { echo "FAIL: agent-sandbox not installed (hack/minikube-up.sh)" >&2; exit 1; }
[ -s "$HOME/.config/hrc/discord.token" ] \
  || { echo "FAIL: ~/.config/hrc/discord.token missing" >&2; exit 1; }
lm_img="ghcr.io/nabi-allenby/hermes-cluster/lifecycle-manager:dev"
if ! minikube image ls | grep -q "lifecycle-manager:dev"; then
  docker image inspect "$lm_img" >/dev/null 2>&1 \
    || (cd ../.. && make image VERSION=dev)
  minikube image load "$lm_img"
fi
if ! minikube image ls | grep -q "hermes-relay-connector.*0.2.0"; then
  minikube image load ghcr.io/nabi-allenby/hermes-relay-connector:0.2.0
fi
minikube image ls | grep -q "hermes-agent.*local" \
  || { echo "FAIL: hermes-agent:local not in minikube (run hack/p-m1/run.sh once)" >&2; exit 1; }

echo ">> secrets (seed + tokens reused from p-m1 tooling) + discord token"
PM1_NAMESPACE="$ns" ../p-m1/make-seed.sh >/dev/null
k create secret generic hrc-discord-token \
  --from-file=token="$HOME/.config/hrc/discord.token" \
  --dry-run=client -o yaml | k apply -f - >/dev/null

echo ">> session template + pool (p-m1 recipe), RBAC, connector, LM"
k apply -f ../p-m1/template.yaml >/dev/null
k apply -f ../e2e/rbac.yaml >/dev/null
k apply -f connector.yaml >/dev/null
k apply -f lm.yaml >/dev/null
k rollout status deploy/hrc --timeout=120s >/dev/null
k rollout status deploy/hlm --timeout=120s >/dev/null

echo ">> discord shard"
pf_pid=""
trap '[ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true' EXIT
shard=""
for _ in $(seq 1 30); do
  if [ -z "$pf_pid" ] || ! kill -0 "$pf_pid" 2>/dev/null; then
    k port-forward svc/hrc 18420:8420 >/dev/null 2>&1 &
    pf_pid=$!
  fi
  shard="$(curl -sf http://127.0.0.1:18420/metrics | grep '^hrc_discord_gateway_up' | awk '{print $2}' || true)"
  [ "$shard" = "1" ] && break
  sleep 1
done
[ "$shard" = "1" ] || { echo "FAIL: hrc_discord_gateway_up=$shard (bad token?)" >&2; exit 1; }
echo "   Discord shard READY"

echo ">> create session $session through the LM"
k port-forward svc/hlm 18080:8080 >/dev/null 2>&1 &
lm_pf=$!
trap '[ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null; kill "$lm_pf" 2>/dev/null || true' EXIT
sleep 1
code="$(curl -s -o /tmp/pac1-create.json -w '%{http_code}' -X POST http://127.0.0.1:18080/v1/sessions \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"$session\",\"idleTimeoutSeconds\":120,\"ttlSeconds\":0,\"displayName\":\"P-AC1 live session\",\"connector\":{}}")"
if [ "$code" = "409" ]; then
  echo "   session already exists — continuing"
elif [ "$code" != "201" ]; then
  echo "FAIL: create returned $code: $(cat /tmp/pac1-create.json)" >&2; exit 1
fi

echo ">> wait for session Ready + gateway connected"
for _ in $(seq 1 120); do
  state="$(curl -sf "http://127.0.0.1:18080/v1/sessions/$session" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("state",""), d.get("connector",{}).get("connected",""))' 2>/dev/null || true)"
  [ "$state" = "Ready True" ] && break
  sleep 2
done
[ "$state" = "Ready True" ] || { echo "FAIL: session state/connected = '$state'" >&2; exit 1; }

admin_token="$(k get secret hrc-tokens -o jsonpath='{.data.admin-token}' | base64 -d)"
curl -sf http://127.0.0.1:18420/admin/v1/instances -H "Authorization: Bearer $admin_token" \
  | python3 -c "import json,sys; i=[x for x in json.load(sys.stdin)['instances'] if x['gatewayId']=='$session'][0]; print('   instance:', i['gatewayId'], '| connected:', i['connected'], '| wakeUrl:', i['wakeUrl'])"

cat <<EOF

P-AC1 READY — the machine side is done. Now the human side:

  1. DM the Discord bot. Expect a reply from the agent.
  2. Stop chatting for ~2-3 min (idle 120s + sweep 30s): the LM suspends
     the session. Watch: kubectl get sandbox $session -w
  3. DM again while suspended: message buffers, connector pokes
     http://hlm:8080/wake/$session, LM patches Running, agent resumes,
     drains, replies. That reply completes P-AC1.

  LM API:    kubectl port-forward svc/hlm 18080:8080  (then /v1/sessions/$session)
  LM logs:   kubectl logs deploy/hlm -f
  Teardown:  hack/p-ac1/down.sh
EOF
