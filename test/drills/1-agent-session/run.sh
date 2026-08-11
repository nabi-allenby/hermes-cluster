#!/usr/bin/env bash
# Agent-session drill: the real hermes-agent as a sandbox session.
#
#   claim -> pod boots -> self-provisions -> gateway connected-idle
#   -> suspend (graceful going_idle) -> message buffers while suspended
#   -> resume -> re-auth from persisted state -> buffer drains
#   -> decommission cascade
#
# Prereqs: minikube running with agent-sandbox installed (test/env/minikube-up.sh),
# images hermes-agent:local and hrc:e2e in the local docker daemon, and a seed
# home directory for make-seed.sh (set DRILL_SEED_DIR).
#
# Env: DRILL_SEED_DIR (required, via make-seed.sh), DRILL_NAMESPACE (default),
# DRILL_ECHO=0 to skip the LLM round-trip check, DRILL_REQUIRE_ECHO=1 to fail
# on it, DRILL_KEEP=1 to leave the connector + template installed after.
set -euo pipefail
cd "$(dirname "$0")"

# Refuse to run against a non-local cluster by accident.
ctx="$(kubectl config current-context 2>/dev/null || true)"
want="${DRILL_CONTEXT:-minikube}"
[ "$ctx" = "$want" ] || { echo "FAIL: kubectl context is '$ctx', expected '$want' (set DRILL_CONTEXT to override)" >&2; exit 1; }

ns="${DRILL_NAMESPACE:-default}"
claim="s-drill-1"
k() { kubectl -n "$ns" "$@"; }
now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

wait_cond() { # object condition timeout_s -> echoes elapsed ms
  local obj="$1" cond="$2" timeout="$3" start elapsed
  start=$(now_ms)
  for _ in $(seq 1 "$((timeout * 2))"); do
    if [ "$(k get "$obj" -o jsonpath="{.status.conditions[?(@.type=='$cond')].status}" 2>/dev/null)" = "True" ]; then
      elapsed=$(( $(now_ms) - start )); echo "$elapsed"; return 0
    fi
    sleep 0.5
  done
  echo "timeout waiting for $obj $cond=True" >&2; return 1
}

# ── admin-API helpers (via port-forward) ─────────────────────────────────
pf_pid=""
cleanup() {
  [ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true
  if [ "${DRILL_KEEP:-0}" != "1" ]; then
    k delete sandboxclaim "$claim" --ignore-not-found --wait=false >/dev/null 2>&1 || true
    k delete -f connector.yaml --ignore-not-found >/dev/null 2>&1 || true
    k delete -f template.yaml --ignore-not-found >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

admin() { # method path [json-body]
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -sf -X "$method" "http://127.0.0.1:18420$path" \
      -H "Authorization: Bearer $admin_token" \
      -H "Content-Type: application/json" -d "$body"
  else
    curl -sf -X "$method" "http://127.0.0.1:18420$path" \
      -H "Authorization: Bearer $admin_token"
  fi
}

instance_field() { # field -> value for our gateway (empty if absent)
  admin GET /admin/v1/instances \
    | python3 -c "import json,sys; xs=[i for i in json.load(sys.stdin)['instances'] if i.get('gatewayId')=='$claim']; print(xs[0].get('$1','') if xs else '')"
}

wait_instance() { # field expected timeout_s -> echoes elapsed ms
  local field="$1" want="$2" timeout="$3" start elapsed got
  start=$(now_ms)
  for _ in $(seq 1 "$((timeout * 2))"); do
    got="$(instance_field "$field" || true)"
    if [ "$got" = "$want" ]; then
      elapsed=$(( $(now_ms) - start )); echo "$elapsed"; return 0
    fi
    sleep 0.5
  done
  echo "timeout: instance $field=$got, wanted $want" >&2; return 1
}

# ── prereqs ──────────────────────────────────────────────────────────────
echo ">> prereqs"
kubectl get crd sandboxclaims.extensions.agents.x-k8s.io >/dev/null \
  || { echo "FAIL: agent-sandbox not installed (run test/env/minikube-up.sh)" >&2; exit 1; }
for img in hermes-agent:local hrc:e2e; do
  if ! minikube image ls | grep -q "${img/:/.*}"; then
    docker image inspect "$img" >/dev/null 2>&1 \
      || { echo "FAIL: image $img not in local docker (build it first)" >&2; exit 1; }
    echo "   loading $img into minikube (one-time; the agent image is ~4GB)"
    minikube image load "$img"
  fi
done

echo ">> secrets + bootstrap"
DRILL_NAMESPACE="$ns" ./make-seed.sh >/dev/null
admin_token="$(k get secret hrc-tokens -o jsonpath='{.data.admin-token}' | base64 -d)"

echo ">> connector"
k apply -f connector.yaml >/dev/null
k rollout status deploy/hrc --timeout=60s >/dev/null
k port-forward svc/hrc 18420:8420 >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 20); do curl -sf http://127.0.0.1:18420/metrics >/dev/null && break; sleep 0.5; done

echo ">> template + pool; cleanup of previous claims"
k delete sandboxclaim "$claim" --ignore-not-found --wait=true >/dev/null 2>&1 || true
k apply -f template.yaml >/dev/null

echo ">> create claim $claim"
t0=$(now_ms)
cat <<EOF | k apply -f - >/dev/null
apiVersion: extensions.agents.x-k8s.io/v1beta1
kind: SandboxClaim
metadata:
  name: $claim
  labels:
    hermes.nabi.dev/managed: "true"
spec:
  warmPoolRef:
    name: hermes-pool
EOF
ready_ms=$(wait_cond "sandboxclaim/$claim" Ready 300)
sandbox="$(k get sandboxclaim "$claim" -o jsonpath='{.status.sandbox.name}')"
[ "$sandbox" = "$claim" ] \
  || { echo "FAIL: cold-start sandbox name ($sandbox) != claim name; gatewayId derivation broken" >&2; exit 1; }
pod="$(k get pods -o name | grep "$sandbox" | head -1 || true)"
[ "${pod#pod/}" = "$claim" ] \
  || { echo "FAIL: pod name (${pod#pod/}) != claim name; gatewayId derivation broken" >&2; exit 1; }
echo "   claim Ready in ${ready_ms}ms; pod == sandbox == claim == gatewayId"

echo ">> wait for connected-idle (self-provision + relay handshake)"
conn_ms=$(wait_instance connected True 180)
boot_total=$(( $(now_ms) - t0 ))
echo "   connected in ${conn_ms}ms after Ready (claim -> connected total ${boot_total}ms)"

if [ "${DRILL_ECHO:-1}" = "1" ]; then
  echo ">> echo round-trip (needs a working LLM key in the seeded .env)"
  admin POST /echo/inbound "{\"gatewayId\":\"$claim\",\"chatId\":\"drill-chat\",\"text\":\"Reply with the single word: pong\"}" >/dev/null
  reply=""
  for _ in $(seq 1 90); do
    reply="$(admin GET /echo/outbox | python3 -c "
import json, sys
acts = [a for a in json.load(sys.stdin)['actions']
        if a.get('kind') == 'outbound' and a.get('gateway_id') == '$claim']
a = acts[-1]['action'] if acts else {}
print(a.get('content') or a.get('text') or '')" 2>/dev/null || true)"
    [ -n "$reply" ] && break
    sleep 2
  done
  if [ -n "$reply" ]; then
    echo "   agent replied: ${reply:0:80}"
  else
    echo "   WARN: no reply in 180s (LLM key missing/out of credits?) — connected-idle already proven"
    [ "${DRILL_REQUIRE_ECHO:-0}" = "1" ] && exit 1
  fi
fi

echo ">> suspend (graceful going_idle expected)"
k patch sandbox "$sandbox" --type=merge -p '{"spec":{"operatingMode":"Suspended"}}' >/dev/null
suspend_ms=$(wait_cond "sandbox/$sandbox" Suspended 120)
disc_ms=$(wait_instance connected False 30)
echo "   Suspended=True in ${suspend_ms}ms; connector saw disconnect in ${disc_ms}ms"

echo ">> buffer a message while suspended"
admin POST /echo/inbound "{\"gatewayId\":\"$claim\",\"chatId\":\"drill-chat\",\"text\":\"buffered while asleep\"}" >/dev/null
buffered="$(instance_field bufferedCount)"
[ "$buffered" -ge 1 ] 2>/dev/null \
  || { echo "FAIL: bufferedCount=$buffered after echo while suspended" >&2; exit 1; }
echo "   bufferedCount=$buffered"

echo ">> resume — re-auth from persisted state"
t2=$(now_ms)
k patch sandbox "$sandbox" --type=merge -p '{"spec":{"operatingMode":"Running"}}' >/dev/null
resume_ms=$(wait_cond "sandbox/$sandbox" Ready 120)
reconn_ms=$(wait_instance connected True 180)
drain_ms=$(wait_instance bufferedCount 0 60)
resume_total=$(( $(now_ms) - t2 ))
echo "   Ready in ${resume_ms}ms; reconnected ${reconn_ms}ms later; buffer drained ${drain_ms}ms after that"

echo ">> decommission (claim delete cascade)"
k delete sandboxclaim "$claim" --wait=true >/dev/null
leftover=""
for _ in $(seq 1 45); do
  leftover="$(k get sandbox,pvc -o name 2>/dev/null | grep "$sandbox" || true)"
  [ -z "$leftover" ] && break
  sleep 2
done
[ -z "$leftover" ] || { echo "FAIL: leftovers after claim delete: $leftover" >&2; exit 1; }

cat <<EOF

AGENT-SESSION DRILL PASS
  cold boot   claim -> pod Ready:          ${ready_ms} ms
              Ready -> gateway connected:  ${conn_ms} ms   (total ${boot_total} ms)
  suspend     patch -> Suspended=True:     ${suspend_ms} ms (graceful gateway exit)
  buffered while suspended:                ${buffered} event(s)
  resume      patch -> pod Ready:          ${resume_ms} ms
              Ready -> reconnected:        ${reconn_ms} ms
              reconnect -> buffer drained: ${drain_ms} ms   (wake total ${resume_total} ms)
  re-auth from persisted state proven; claim delete cascaded sandbox+PVC
EOF
