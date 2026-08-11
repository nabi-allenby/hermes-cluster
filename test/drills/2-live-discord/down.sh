#!/usr/bin/env bash
# Tear down the live-Discord drill: session via the LM (deprovision +
# cascade), then the chart release. Keeps hermes-home-seed; removes the
# Discord token secret.
set -euo pipefail
ns="${DRILL_NAMESPACE:-hermes}"
session="${DRILL_SESSION:-s-live-1}"
# Refuse to run against a non-local cluster by accident.
ctx="$(kubectl config current-context 2>/dev/null || true)"
want="${DRILL_CONTEXT:-minikube}"
[ "$ctx" = "$want" ] || { echo "FAIL: kubectl context is '$ctx', expected '$want' (set DRILL_CONTEXT to override)" >&2; exit 1; }

k() { kubectl -n "$ns" "$@"; }

if k get deploy hermes-hlm >/dev/null 2>&1; then
  k port-forward svc/hermes-hlm 18080:8080 >/dev/null 2>&1 &
  pf=$!
  trap '[ -n "$pf" ] && kill "$pf" 2>/dev/null || true' EXIT
  sleep 2
  curl -s --max-time 10 -X DELETE "http://127.0.0.1:18080/v1/sessions/$session" >/dev/null || true
  kill "$pf" 2>/dev/null || true
fi
k delete sandboxclaim "$session" --ignore-not-found --wait=false >/dev/null 2>&1 || true
helm uninstall hermes -n "$ns" >/dev/null 2>&1 || true
k delete secret hrc-discord-token --ignore-not-found >/dev/null
echo "live-discord drill torn down (hermes-home-seed kept; discord token secret removed)"
