#!/usr/bin/env bash
# Start (or reuse) a minikube cluster and install the pinned agent-sandbox release.
set -euo pipefail

AGENT_SANDBOX_VERSION="${AGENT_SANDBOX_VERSION:-v0.5.4}"
PROFILE="${MINIKUBE_PROFILE:-minikube}"
MANIFEST="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox-with-extensions.yaml"

if minikube -p "$PROFILE" status >/dev/null 2>&1; then
  echo ">> reusing running minikube profile=$PROFILE"
else
  # An existing (stopped) profile keeps its saved driver; a fresh one uses
  # MINIKUBE_DRIVER or minikube's own default.
  echo ">> starting minikube profile=$PROFILE"
  minikube start -p "$PROFILE" ${MINIKUBE_DRIVER:+--driver="$MINIKUBE_DRIVER"}
fi

kubectl config use-context "$PROFILE" >/dev/null

echo ">> installing agent-sandbox ${AGENT_SANDBOX_VERSION}"
kubectl apply -f "$MANIFEST"

echo ">> waiting for agent-sandbox controller"
kubectl rollout status deploy/agent-sandbox-controller -n agent-sandbox-system --timeout=180s

# The SandboxClaim CRD uses a conversion webhook served by the controller; claims
# cannot be listed/created until it answers.
echo ">> waiting for conversion webhook"
for _ in $(seq 1 60); do
  if kubectl get sandboxclaims.extensions.agents.x-k8s.io -A >/dev/null 2>&1; then
    echo ">> agent-sandbox ready"
    exit 0
  fi
  sleep 2
done
echo "conversion webhook never became responsive" >&2
exit 1
