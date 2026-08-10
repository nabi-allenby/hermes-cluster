#!/usr/bin/env bash
# Tear down the P-AC1 live demo: session via the LM (deprovision + cascade),
# then the deployments. Keeps the seed/token secrets (make-seed reuses them).
set -euo pipefail
cd "$(dirname "$0")"

ns="${PAC1_NAMESPACE:-default}"
session="${PAC1_SESSION:-s-live-1}"
k() { kubectl -n "$ns" "$@"; }

if k get deploy hlm >/dev/null 2>&1; then
  k port-forward svc/hlm 18080:8080 >/dev/null 2>&1 &
  pf=$!
  sleep 1
  curl -s -X DELETE "http://127.0.0.1:18080/v1/sessions/$session" >/dev/null || true
  kill "$pf" 2>/dev/null || true
fi
k delete sandboxclaim "$session" --ignore-not-found --wait=false >/dev/null 2>&1 || true
k delete -f lm.yaml -f connector.yaml --ignore-not-found >/dev/null 2>&1 || true
k delete -f ../p-m1/template.yaml --ignore-not-found >/dev/null 2>&1 || true
k delete secret hrc-discord-token --ignore-not-found >/dev/null
echo "P-AC1 demo torn down (seed/token secrets kept)"
