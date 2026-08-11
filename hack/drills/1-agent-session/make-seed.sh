#!/usr/bin/env bash
# Build the Secrets/ConfigMap the P-M1 drill needs. Nothing here is committed;
# all secret material lives only in the cluster.
#
#   hermes-home-seed        first-boot HERMES_HOME content: auth.json,
#                           config.yaml, SOUL.md, and .env stripped of the
#                           GATEWAY_RELAY_* lines (the boot-time self-provision
#                           owns those)
#   hrc-tokens              connector admin + provision tokens (generated,
#                           reused if the Secret already exists)
#   hermes-provision-token  the provision token again, mounted into agent pods
#   hermes-bootstrap        bootstrap.py (ConfigMap, from this directory)
#
# Usage: make-seed.sh [seed-dir]     (default: ~/Downloads/.hermes-container-home,
#                                     the home proven by the connector's Docker demo)
set -euo pipefail
cd "$(dirname "$0")"

ns="${PM1_NAMESPACE:-default}"
seed_dir="${1:-$HOME/Downloads/.hermes-container-home}"
k() { kubectl -n "$ns" "$@"; }

for f in auth.json config.yaml SOUL.md .env; do
  [ -e "$seed_dir/$f" ] || { echo "FAIL: $seed_dir/$f missing" >&2; exit 1; }
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
grep -v '^GATEWAY_RELAY_' "$seed_dir/.env" > "$tmp/env" || true

echo ">> hermes-home-seed (from $seed_dir)"
k create secret generic hermes-home-seed \
  --from-file=auth.json="$seed_dir/auth.json" \
  --from-file=config.yaml="$seed_dir/config.yaml" \
  --from-file=SOUL.md="$seed_dir/SOUL.md" \
  --from-file=.env="$tmp/env" \
  --dry-run=client -o yaml | k apply -f - >/dev/null

echo ">> hrc-tokens + hermes-provision-token"
if k get secret hrc-tokens >/dev/null 2>&1; then
  prov_token="$(k get secret hrc-tokens -o jsonpath='{.data.provision-token}' | base64 -d)"
else
  admin_token="$(openssl rand -hex 24)"
  prov_token="$(openssl rand -hex 24)"
  k create secret generic hrc-tokens \
    --from-literal=admin-token="$admin_token" \
    --from-literal=provision-token="$prov_token" >/dev/null
fi
k create secret generic hermes-provision-token \
  --from-literal=token="$prov_token" \
  --dry-run=client -o yaml | k apply -f - >/dev/null

echo ">> hermes-bootstrap ConfigMap (canonical copy lives in the chart)"
k create configmap hermes-bootstrap \
  --from-file=bootstrap.py=../../../charts/hermes-cluster/files/bootstrap.py \
  --dry-run=client -o yaml | k apply -f - >/dev/null

echo "seed + tokens ready in namespace $ns (no values printed)"
