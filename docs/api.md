# lifecycle-manager HTTP API

Listener: `HLM_LISTEN` (default `:8080`). All bodies and responses are JSON;
errors are `{"error": "<message>"}`. When `HLM_API_TOKEN(_FILE)` is set, `/v1/*`
requires `Authorization: Bearer <token>`. `/wake/*`, `/healthz`, `/readyz`, and
`/status` are never authenticated — the connector's wake poke is a bare GET.

## Sessions

### `POST /v1/sessions`

```jsonc
{
  "id": "s-abc123",              // optional; generated ("s-<10 hex>") when absent.
                                 // DNS-1123 label; doubles as claim name and gatewayId.
  "warmPool": "my-pool",         // optional; default HLM_WARM_POOL
  "ttlSeconds": 86400,           // optional; default HLM_TTL; 0 = immortal
  "idleTimeoutSeconds": 1800,    // optional; default HLM_IDLE_TIMEOUT; 0 = never suspend
  "displayName": "Dan's agent",  // optional
  "connector": {                 // optional; presence requests relay provisioning
    "routeKeys": [ {"platform": "discord", "chatId": "123456789"} ]
  }
}
```

Creates a `SandboxClaim` (labeled `hermes.nabi.dev/managed=true`; knobs stored
as annotations). With a `connector` block, also provisions the instance in the
relay connector (gatewayId = session id, wakeUrl = `HLM_WAKE_BASE_URL/wake/<id>`)
and binds the routes. The returned gateway secret is discarded — the agent pod
self-provisions on boot and rotates it.

Responses: `201` session object · `400` invalid id/body · `409` id exists ·
`422` connector block while integration disabled · `502` provision failed (the
claim is rolled back) · `503` connector throttled.

### `GET /v1/sessions` / `GET /v1/sessions/{id}`

`200 {"sessions": [Session]}` / `200 Session` · `404`.

```jsonc
// Session
{
  "id": "s-abc123",
  "state": "Ready",              // Pending|Ready|Waking|Suspended|Suspending|Terminating (derived, never stored)
  "operatingMode": "Running",
  "createdAt": "2026-08-10T17:00:00Z",
  "ttlSeconds": 86400,
  "idleTimeoutSeconds": 1800,
  "displayName": "Dan's agent",
  "connector": {                 // only when the session was provisioned
    "provisioned": true,
    "connected": false,
    "revoked": false,
    "bufferedCount": 2,
    "lastInboundAt": 1754800000, // unix seconds; null = unknown
    "turnInFlight": false
  }
}
```

### `DELETE /v1/sessions/{id}`

The single decommission path (same code the TTL sweeper calls): connector
deprovision first (`DELETE /admin/v1/instances/{id}` — purges buffer, routes,
and the gateway row), then claim deletion (cascades sandbox, pod, service,
PVC). If deprovision fails the claim is **kept** and the delete returns `502`;
retrying (or the next TTL sweep) picks it back up.

`200 {"deleted": true, "deprovisioned": bool}` · `404` · `502` · `503`.

## Wake

### `GET|POST /wake/{session}`

Unconditionally patches the session's sandbox to `operatingMode: Running` and
returns immediately. Idempotent; safe mid-suspension. This URL is what the
lifecycle-manager registers as the connector's `wakeUrl`.

`200 {"ok": true}` · `404 unknown session` · `500` (connector's next
cooldown-gated poke retries; a lost poke degrades to delivery-on-next-resume).

## Health / status

- `GET /healthz` — `200 ok` (process liveness).
- `GET /readyz` — `200` when the Kubernetes API answers, else `503`.
- `GET /status` — session counts by state, connector reachability/throttle,
  and the latest reconcile report:

```jsonc
{
  "sessions": 3,
  "byState": {"Ready": 2, "Suspended": 1},
  "connector": {"enabled": true, "throttledUntil": 1754800065},
  "reconcile": {
    "claimsWithoutInstances": [],   // provisioned sessions missing on the connector
    "instancesWithoutClaims": [],   // connector rows with no managed claim (report-only)
    "lastRun": 1754800000
  }
}
```
