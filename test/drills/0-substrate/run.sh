#!/usr/bin/env bash
# P-M0: claim -> Ready -> suspend -> PVC survives -> resume -> marker intact.
# Prints a timing table at the end.
set -euo pipefail
cd "$(dirname "$0")"

ns="${PM0_NAMESPACE:-default}"
k() { kubectl -n "$ns" "$@"; }

now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

wait_cond() { # object condition timeout_s
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

echo ">> cleanup from previous runs"
k delete sandboxclaim hello-1 --ignore-not-found --wait=true >/dev/null 2>&1 || true

echo ">> apply template + pool + claim"
t0=$(now_ms)
k apply -f hello-world.yaml >/dev/null
ready_ms=$(wait_cond sandboxclaim/hello-1 Ready 180)
sandbox="$(k get sandboxclaim hello-1 -o jsonpath='{.status.sandbox.name}')"
echo "   claim Ready in ${ready_ms}ms, bound sandbox: $sandbox"

pod="$(k get pods -l 'agents.x-k8s.io/sandbox-name' -o name 2>/dev/null | head -1 || true)"
[ -z "$pod" ] && pod="$(k get pods -o name | grep "$sandbox" | head -1)"
echo "   pod: $pod"
k wait --for=condition=Ready "$pod" --timeout=60s >/dev/null
marker_before="$(k exec "${pod#pod/}" -- cat /data/marker)"
echo "   marker: $marker_before"

echo ">> suspend"
t1=$(now_ms)
k patch sandbox "$sandbox" --type=merge -p '{"spec":{"operatingMode":"Suspended"}}' >/dev/null
suspend_ms=$(wait_cond "sandbox/$sandbox" Suspended 120)
pvcs="$(k get pvc -o name | grep "$sandbox" || true)"
echo "   Suspended=True in ${suspend_ms}ms; surviving PVCs: ${pvcs:-NONE}"
[ -n "$pvcs" ] || { echo "FAIL: PVC did not survive suspend" >&2; exit 1; }
if k get pods -o name | grep -q "$sandbox"; then
  echo "FAIL: pod still exists after Suspended=True" >&2; exit 1
fi

echo ">> resume"
t2=$(now_ms)
k patch sandbox "$sandbox" --type=merge -p '{"spec":{"operatingMode":"Running"}}' >/dev/null
resume_ms=$(wait_cond "sandbox/$sandbox" Ready 120)
pod="$(k get pods -o name | grep "$sandbox" | head -1)"
k wait --for=condition=Ready "$pod" --timeout=60s >/dev/null
marker_after="$(k exec "${pod#pod/}" -- cat /data/marker)"
echo "   Ready=True in ${resume_ms}ms; marker after resume: $marker_after"

[ "$marker_before" = "$marker_after" ] || { echo "FAIL: marker changed across suspend/resume" >&2; exit 1; }

echo ">> decommission (claim delete cascade)"
k delete sandboxclaim hello-1 --wait=true >/dev/null
# Garbage collection is asynchronous; give the cascade up to 90 s.
leftover=""
for _ in $(seq 1 45); do
  leftover="$(k get sandbox,pvc -o name 2>/dev/null | grep "$sandbox" || true)"
  [ -z "$leftover" ] && break
  sleep 2
done
[ -z "$leftover" ] || { echo "FAIL: leftovers after claim delete: $leftover" >&2; exit 1; }

cat <<EOF

P-M0 PASS
  cold start (claim -> Ready):   ${ready_ms} ms
  suspend  (patch -> Suspended): ${suspend_ms} ms
  resume   (patch -> Ready):     ${resume_ms} ms
  marker survived suspend/resume; claim delete cascaded sandbox+PVC
EOF
