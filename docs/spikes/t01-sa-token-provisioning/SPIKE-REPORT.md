# T01 spike — ServiceAccount-token provisioning (evidence for locked decision D4)

**Status:** spike complete — all five questions answered with live-cluster evidence (kind v1.36.1 + minikube v1.35.0); recommendation: **TokenReview**, with the lifecycle-manager keeping a privileged create-time provision path. Awaiting G1 sign-off (human acceptance of this report) before any tenancy implementation work.
**Date:** 2026-08-14
**Scope:** this document is the report of a time-boxed spike (an investigation ending in proof-of-concept evidence plus a recommendation — no production code). It covers replacing the shared provision token — the one 48-character secret that today authenticates every `POST /relay/provision` call to the relay connector — with each session pod's own projected ServiceAccount token, so that users of one installation need not trust each other (the migration plan's locked decision **D4**, "retire the shared provision token"). Nothing in the chart, the lifecycle-manager, or the connector changes here; the PoC artifacts under `poc/` are evidence, not shippable code.

---

## 1. What this is

Today one shared token authenticates all provisioning: it is generated in `charts/hermes-cluster/templates/tokens-secret.yaml`, projected into every session pod at `/run/hermes/provision-token` (`templates/session-template.yaml:145-158`), and compared statically by the connector (`check_static_token`, `src/http_api.rs:171-180`) with **no binding between the caller and the `gatewayId` it presents**. Any session pod — that is, any user of the installation — can therefore provision (and thereby rotate the relay credentials of) **any other user's session**. That is the tenancy hole decision D4 closes.

The candidate replacement: Kubernetes already gives every pod a cryptographically verifiable, short-lived, audience-scoped identity — a **projected ServiceAccount token** whose claims name the exact pod it is bound to. If the connector verifies that token and enforces `token.pod.name == gatewayId`, a session pod can provision *itself* and nothing else.

This spike answers five questions with running-cluster evidence and ends with a recommendation. It is the input to sign-off gate G1.

## 2. Environment and artifacts

| Component | Version |
|---|---|
| kind | v0.32.0, node image Kubernetes **v1.36.1** |
| minikube | v1.38.0 (docker driver), Kubernetes **v1.35.0** |
| agent-sandbox | **v0.5.4** (`sandbox-with-extensions.yaml` release manifest, the repo's pin) |
| PoC verifier | Go 1.26.4, stdlib only |

PoC artifacts (all under `docs/spikes/t01-sa-token-provisioning/poc/`):

- `template.yaml` — modified copy of `test/fixtures/template.yaml`: adds a `ServiceAccount`, `serviceAccountName`, and a `serviceAccountToken` projected volume source (audience `hermes-relay-connector`, `expirationSeconds: 600`); plus a replicas-1 warm pool for the adoption test.
- `claims.yaml`, `claim-warm.yaml` — a cold-start claim (`s-dc-843921`) and a warm-adoption claim (`s-dc-777001`).
- `connector-rbac.yaml` — the *complete* RBAC a TokenReview-verifying connector needs: one ServiceAccount + `create` on `authentication.k8s.io/tokenreviews`. Nothing else.
- `verify.go` — a stdlib-only model of a JWKS-verifying connector: RS256 signature against the cluster JWKS, issuer/audience/expiry checks, then `token."kubernetes.io".pod.name == gatewayId`.

Both clusters ran the identical fixture set. Transcripts below are trimmed to the relevant fields but otherwise verbatim.

## 3. Q1 — does agent-sandbox v0.5.4 pass through `serviceAccountName` and a projected `serviceAccountToken` volume?

**Yes, verbatim, on both clusters.** The rendered pod for cold-start claim `s-dc-843921` on kind:

```json
{
  "serviceAccountName": "t01-session",
  "volumes": [{
    "name": "sa-token",
    "projected": {
      "defaultMode": 292,
      "sources": [{
        "serviceAccountToken": {
          "audience": "hermes-relay-connector",
          "expirationSeconds": 600,
          "path": "token"
        }
      }]
    }
  }],
  "mounts": [{"mountPath": "/run/hermes", "name": "sa-token", "readOnly": true}]
}
```

(minikube: identical.) The token file appears at `/run/hermes/token` inside the container, kubelet-refreshed. Two details from `session-template.yaml` that motivated this check:

- `envVarsInjectionPolicy: Disallowed` (line 18) governs **claim-level `spec.env` injection** only; it does not interfere with template-declared volumes. The token arrives as a file, exactly like today's shared token, so the policy is a non-issue — and the existing file-first secret discipline is preserved.
- The production template sets no `serviceAccountName` today; adding one is a one-line template change plus one ServiceAccount object. (Production should additionally set `automountServiceAccountToken: false` so session pods carry *only* the connector-audience token and no API-server credential — the PoC did not bother.)

## 4. Q2 — which verification mechanism should the connector use?

### 4.1 What the token carries vs. what TokenReview returns

Decoded claims of the projected token (kind; minikube identical apart from timestamps/uids):

```json
{
  "aud": ["hermes-relay-connector"],
  "iss": "https://kubernetes.default.svc.cluster.local",
  "iat": 1786657002, "exp": 1786657602,          // exactly 600 s — the
  "sub": "system:serviceaccount:default:t01-session",  // "extend to 1 yr" legacy
  "kubernetes.io": {                              // behavior does NOT apply to
    "namespace": "default",                       // custom audiences
    "node": {"name": "t01-spike-control-plane", "uid": "755bcd5a-…"},
    "pod":  {"name": "s-dc-843921", "uid": "1bb0416f-…"},
    "serviceaccount": {"name": "t01-session", "uid": "99642281-…"}
  }
}
```

So **in the token itself**, the pod name lives in the `kubernetes.io` bound-object claims: `claims["kubernetes.io"].pod.{name,uid}`.

A TokenReview of the same token (POSTed to `/apis/authentication.k8s.io/v1/tokenreviews` as the connector's own ServiceAccount, with `spec.audiences: ["hermes-relay-connector"]`):

```json
{
  "authenticated": true,
  "username": "system:serviceaccount:default:t01-session",
  "audiences": ["hermes-relay-connector"],
  "extra": {
    "authentication.kubernetes.io/credential-id": ["JTI=0b3fa733-…"],
    "authentication.kubernetes.io/node-name": ["t01-spike-control-plane"],
    "authentication.kubernetes.io/node-uid":  ["755bcd5a-…"],
    "authentication.kubernetes.io/pod-name": ["s-dc-843921"],
    "authentication.kubernetes.io/pod-uid":  ["1bb0416f-…"]
  }
}
```

So **in a TokenReview response**, the pod name lives in `status.user.extra["authentication.kubernetes.io/pod-name"]`. Both surfaces carry it; the enforcement `pod name == gatewayId` is expressible either way.

⚠️ One trap, proven live: a ServiceAccount token that is **not pod-bound** (e.g. minted by `kubectl create token` with no bound object, or any credential a user extracts some other way) reviews as `authenticated: true` with **no pod-name extra at all**. A TokenReview-based connector must therefore require the pod-name extra to be present and equal to the `gatewayId` — `authenticated: true` alone is not an authorization.

### 4.2 Issuer/JWKS reachability, measured on both clusters

| Probe | kind v1.36.1 | minikube v1.35.0 |
|---|---|---|
| issuer URL in tokens | `https://kubernetes.default.svc.cluster.local` | same |
| advertised `jwks_uri` | `https://172.19.0.2:6443/openid/v1/jwks` (node-internal IP) | `https://192.168.49.2:8443/openid/v1/jwks` (node-internal IP) |
| `/openid/v1/jwks`, anonymous, from outside | **HTTP 403** | **HTTP 403** |
| `/.well-known/openid-configuration`, anonymous, from outside | **HTTP 403** | **HTTP 403** |
| discovery + JWKS from **inside** the cluster, via the issuer URL, with any authenticated ServiceAccount token + cluster CA | **HTTP 200 / 200** | **HTTP 200 / 200** |
| JWKS from inside, no bearer | **HTTP 403** | **HTTP 403** |

Readings:

- On both local clusters the issuer URL only resolves *inside* the cluster (it is the API server's service DNS name), and the advertised `jwks_uri` points at a node-internal IP. A JWKS-verifying connector deployed in-cluster *can* fetch the keys — but only by authenticating with a ServiceAccount token of its own (anonymous discovery is 403 under stock kubeadm RBAC, both clusters). So the "no Kubernetes dependency" appeal of JWKS verification is illusory here: **the connector needs cluster credentials either way.**
- The two local cluster flavors behave **identically** — but clouds do not: on AKS/EKS/GKE with an OIDC-issuer feature enabled the issuer is a public HTTPS URL, and without it it's the in-cluster URL again. A JWKS deployment therefore needs per-platform issuer/JWKS configuration and key-rotation refetch logic. A TokenReview deployment needs *none of that*: `kubernetes.default.svc` is reachable from inside every conformant cluster, the API server holds the keys, and the request shape never varies. **That is the kind-vs-cloud consistency argument, and it favors TokenReview.**

### 4.3 PoC runs and the failure-mode matrix

Happy path (kind shown; minikube identical) — out-of-cluster JWKS verification enforcing the binding:

```
ok   [alg] RS256, kid=QXng8RXKiag49P0rNgjpmRqLGIIs5vYIirL-yFFFmPU
ok   [jwks] matched kid in JWKS (1 key(s))
ok   [sig] RS256 signature valid
ok   [iss] https://kubernetes.default.svc.cluster.local
ok   [aud] [hermes-relay-connector] includes "hermes-relay-connector"
ok   [exp] valid for another 517s
ok   [bind] pod-bound: pod=s-dc-843921 uid=1bb0416f-… sa=t01-session ns=default
ok   [gateway] token.pod.name == gatewayId (s-dc-843921) — provision allowed
VERDICT: ACCEPT
```

TokenReview happy path: §4.1 above (`authenticated: true`, pod-name extra `s-dc-843921`). RBAC negative control: the same TokenReview POST authenticated as a ServiceAccount *without* the `create tokenreviews` binding is refused — `403 Forbidden, "…cannot create resource \"tokenreviews\"…"` — confirming `connector-rbac.yaml` is exactly the needed privilege.

Failure modes, all exercised live on kind (expiry also on minikube):

| Failure mode | JWKS verifier | TokenReview |
|---|---|---|
| **Wrong audience** (token minted for `other-system`) | `FAIL [aud] audience [other-system] does not include "hermes-relay-connector"` | error: `token audiences ["hermes-relay-connector"] is invalid for the target audiences ["something-else"]` |
| **Expired token** (600 s elapsed; pod still running) | `FAIL [exp] token expired 10s ago (exp=1786657836 now=1786657846)` — rejected at the first probe | rejected: `authenticated: null`, error `service account token has expired` — but only once past the API server's clock-skew allowance; see the expiry-precision note below |
| **Deleted pod** (claim deleted → cascade; token still 6½ min from expiry) | **`VERDICT: ACCEPT`** — signature valid, exp in the future, claims intact. JWKS verification *cannot see* the deletion. | error: **`service account token has been invalidated`** — rejected immediately |
| **Not pod-bound** (`kubectl create token`, no bound object) | `FAIL [bind] no kubernetes.io/pod claim` | `authenticated: true` but **no pod-name extra** → connector must reject (see §4.1 trap) |
| **Warm-adopted pod presenting the session id** (§6) | `FAIL [gateway] token.pod.name "t01-pool-warm-4l8gw" != presented gatewayId "s-dc-777001"` | same comparison on the pod-name extra — fails identically |

The deleted-pod row is the decisive one: under pure JWKS verification, a token exfiltrated from a pod remains a valid provision credential for up to its remaining lifetime (≤10 minutes at the Kubernetes minimum `expirationSeconds` of 600) *after the session is destroyed*. TokenReview closes that window to zero because the API server invalidates bound tokens the moment the bound pod goes away — proven above with a token still ~6½ minutes from expiry.

**Expiry-precision note (measured, both clusters).** The API server validates `exp` with a clock-skew allowance: minikube's TokenReview still answered `authenticated: true` for a token 10 s and 53 s past its `exp` claim, then rejected it at 134 s past (`service account token has expired`); a bracket probe on kind (token of a live pod reviewed at fixed offsets past `exp`) landed in the same window — see the transcript appended in §4.3.1. This is consistent with the standard ~1-minute JWT clock-skew leeway, applies only while the pod is still alive (deletion invalidates instantly regardless), and is not security-material for this design: an alive pod can mint a fresh token at will anyway. It does mean a zero-leeway JWKS implementation (like `poc/verify.go`, which rejected at +10 s) is *stricter* on this one axis — the only axis where JWKS came out ahead, and only by a minute.

#### 4.3.1 kind leeway bracket probe

A fresh projected token was extracted from a live kind pod and reviewed at fixed offsets past its `exp` claim (host, kind-node, and minikube-node clocks verified in sync to ±1 s first):

```
kind TokenReview at exp+18s (pod alive):
{"authenticated":true,"error":null}
kind TokenReview at exp+90s (pod alive):
{"authenticated":null,"error":"[invalid bearer token, service account token has expired]"}
```

Combined with minikube's accept-at-+10s/+53s, reject-at-+134s, both clusters land in the same accept-below-~60s / reject-above-~60s window.

### 4.4 Recommendation: TokenReview

**Use TokenReview** (option one). Grounds, in order of weight:

1. **Revocation**: deleted-pod tokens die instantly (proven, §4.3). JWKS leaves a ≤10-minute impersonation window and no remediation short of key rotation.
2. **Consistency**: one request shape on kind, minikube, and every conformant cloud; no per-platform issuer/JWKS configuration, no key-rotation logic (measured, §4.2).
3. **No credential advantage for JWKS**: anonymous JWKS fetch is 403 on stock clusters, so both options require the connector to hold cluster credentials (measured, §4.2).
4. **Cost is negligible**: one HTTPS POST to the API server per provision, and provisions happen once per pod boot, not per message. The connector needs no Kubernetes client library — the PoC did it with plain HTTPS + the pod's own mounted credentials; RBAC is the two-object minimum in `connector-rbac.yaml`.
5. The enforcement itself is one string comparison: `extra["authentication.kubernetes.io/pod-name"][0] == gatewayId`, **plus a presence check** (absent pod-name extra ⇒ reject).

The connector-side k8s verification must be **config-selected and default-off** — the connector is a standalone product with an explicit no-Kubernetes acceptance bar (its own `docs/DESIGN.md` §2 G-AC; not to be confused with the Blueprint acceptance criteria). See §7.

## 5. Q3 — what happens to the lifecycle-manager's create-time provision?

Today `Manager.Create` (`internal/lifecycle/manager.go:118-158`) provisions the connector instance *before any pod exists* — registering `wakeUrl` and binding route keys — and **rolls the claim back if provisioning fails** (`manager.go:147-156`). Two candidate flows:

**Flow A — the lifecycle-manager keeps a privileged provision path (recommended).**
The LM authenticates its create-time `POST /relay/provision` with **its own projected ServiceAccount token** (same audience, same TokenReview verification). The connector keeps a small allowlist of privileged ServiceAccount usernames — for the chart, `system:serviceaccount:<ns>:<release>-lifecycle-manager` (the LM already runs under that dedicated ServiceAccount, `templates/lifecycle-manager.yaml:53`). A privileged caller may provision any `gatewayId`; a session-pod caller must satisfy the pod-name binding. One verification path, two roles, **zero static tokens left**. Everything else is untouched: create-time rollback, create-time `SetRoute`, `wakeUrl` registered from birth, `bootstrap.py`'s boot-time re-provision (now self-authenticating), COALESCE semantics.

**Flow B — defer provisioning to first pod boot (rejected).** Instance creation makes only the claim; the pod's first boot creates the connector instance; the LM sets `wakeUrl` afterward via the already-implemented `PATCH /admin/v1/instances/{id}` (client: `internal/connector/http.go:164-166`). Ordering problems, all verified in source:

- `PATCH` answers **404 until the instance exists** (`src/http_api.rs:516-518`), and `SetRoute` *also* 404s on an unknown gateway (`src/http_api.rs:814-816`) — so both wakeUrl and route bindings must wait for first boot.
- The LM may not retry on its own: the no-retries connector discipline makes the sweep cadence the only sanctioned retry loop, so wakeUrl/route convergence rides the sweeper and lands **minutes** after create.
- In the gap, inbound messages for the session have no instance and no route: the connector **drops them (fail-closed)** instead of buffering, and wake-on-buffered-message cannot fire because no `wakeUrl` is registered. Today's flow buffers from birth.
- `Create`'s transactional rollback disappears: a claim whose pod can never provision lingers as a half-session, with no actor responsible for reaping it.
- The LM would need to remember desired routes/wakeUrl until convergence — state with no home, since the claim-is-the-database rule (stateless LM) would force it into new claim annotations.

Flow B trades away rollback, buffering-from-birth, and wake registration for no security gain over Flow A. **Recommend Flow A.**

## 6. Q4 — warm-pool interaction, for the record

Live on kind: claim `s-dc-777001` against a replicas-1 pool adopted the pre-warmed spare — `status.sandbox.name: t01-pool-warm-4l8gw`, pod name likewise. The adopted pod's projected token says `pod.name = "t01-pool-warm-4l8gw"`; a provision presenting the *session* id is rejected (§4.3 last row). **The check fails closed for warm-adopted pods.** Constraint, stated explicitly: under ServiceAccount-token provisioning, a warm-adopted pod can never provision under its session's id — pod-bound identity is inherently cold-start-only, exactly like the existing "one id everywhere" invariant (session id = claim name = pod name = `gatewayId`), which already holds only on cold starts. `session.warmReplicas` **must stay 0**, and this design deepens that requirement rather than relaxing it. (Residual note: a warm pod could still provision under its *own pod name* — identical to today's behavior with the shared token, excluded by the same `warmReplicas: 0` setting, and now at least attributable to the exact pod.)

## 7. Q5 — standalone mode is preserved

Confirmed in source: leaving `HRC_PROVISION_TOKEN` unset already disables the provision endpoint entirely (`src/config.rs` — the `provision_token: Option<String>` doc at lines 20-27 and `load_secret` at line 134; handler guard at `src/http_api.rs:280-285`), and the enrollment flow (`POST /admin/v1/enroll-tokens` + `/relay/enroll`) is independent of provisioning. The design keeps three config-selected provision modes — **disabled** (default), **static token** (today's, for non-Kubernetes hosts), **Kubernetes TokenReview** (new, what the chart selects) — with enroll always available. No existing deployment mode is removed.

## 8. Recommended target design (sketch for G1 — not implemented)

1. **Chart**: session ServiceAccount + `serviceAccountName` + projected `serviceAccountToken` volume (audience e.g. `hermes-relay-connector`, `expirationSeconds: 600`) replacing the `provision-token` projection in `session-template.yaml`; `automountServiceAccountToken: false` on session pods; LM deployment gains the same projected-token volume; connector ServiceAccount + the `tokenreviews` ClusterRole from `poc/connector-rbac.yaml`; `provision-token` leaves `tokens-secret.yaml` (`admin-token` stays — the admin plane is a separate concern, out of D4's scope).
2. **Connector**: new opt-in mode (e.g. `HRC_PROVISION_K8S=1` + audience + privileged-username allowlist): verify the provision bearer via TokenReview using the connector's own mounted credentials; require `authenticated` **and** pod-name extra `== gatewayId`, or username ∈ allowlist for any id. Rotation-on-reprovision stays; `HRC_PROVISION_NEW_ONLY` becomes unnecessary in this mode (its threat — a leaked shared token rotating someone's live instance — no longer exists, and a pod re-provisioning its own id every boot is the *intended* use).
3. **`bootstrap.py`**: read `/run/hermes/token` instead of `/run/hermes/provision-token`. Everything else (COALESCE reliance, 401/403/409 hard-exit) unchanged.
4. **LM**: send its projected token as the provision bearer instead of `HLM_CONNECTOR_PROVISION_TOKEN`.

## 9. Fallback consequences, written out

- **If create-only provisioning were used as the tenancy fix instead** (i.e. `HRC_PROVISION_NEW_ONLY` on, keeping the shared token): it **breaks `bootstrap.py`'s re-provision-on-every-boot behavior** — the connector answers 409 for the existing id and bootstrap deliberately hard-exits on 409 (`files/bootstrap.py:53-58`, the guard that avoids feeding the connector's per-source-IP auth throttle). Every resume after the first boot would crash-loop the session. Create-only mode also leaves the cross-tenant read of the *shared* secret in place — it narrows the blast radius (no rotation hijack of live ids) without removing the shared credential. Not a substitute for D4.
- **If JWKS verification were chosen instead of TokenReview**: a token stolen in the last 10 minutes of a session's life (or the whole token file, exfiltrated any time before deletion) keeps provisioning-as-that-session working for up to 10 minutes after the pod is gone (§4.3, deleted-pod row); plus per-platform issuer configuration and key-rotation refetch logic land in the connector.
- **If the spike's recommendation is rejected entirely** (keep the shared token): decision D4's goal — users of one installation need not trust each other — is unmet; any session pod can rotate any other session's relay credentials and impersonate it.

## 10. Invariants touched, and how each is preserved

- **No-retries connector discipline**: Flow A adds no new call sites and no retry loops; TokenReview calls are connector→API-server, not LM→connector. Flow B was rejected partly *because* it needed retry-shaped convergence.
- **COALESCE provision semantics**: boot-time re-provision still omits `wakeUrl`; the LM still registers it at create. Unchanged in Flow A.
- **Stateless LM (claim-is-the-database)**: Flow A keeps provisioning transactional inside `Create`; no new stored state. (Flow B would have violated it — one reason it was rejected.)
- **One id everywhere / cold-start-only**: the pod-name binding *is* that invariant, enforced cryptographically; `warmReplicas: 0` stays (§6).
- **Exactly one destruction path**: untouched — TokenReview invalidation on pod deletion is an authentication effect, not a second destruction path; `lifecycle.Manager.Decommission` remains the only actor.
- **`/wake/{session}` unauthenticated bare GET**, **never-401 edge**, **idle guards / status-poll purity**: out of this spike's blast radius; no change proposed to any of them. (The connector's provision endpoint returning 401 to a bad token is the existing behavior of an internal API and is unrelated to the dashboard edge rule.)

## 11. Open questions (for G1)

1. Audience string: proposal `hermes-relay-connector` (used throughout the PoC). Should it be release-scoped (e.g. include the release name) so two installations in one cluster can't accept each other's tokens? Cheap to do; decide at implementation.
2. Namespace check: should the connector also pin `kubernetes.io.namespace` (via the TokenReview username's namespace segment) to its own release namespace? Recommended yes at implementation; the PoC enforced only the pod-name binding the task specified.
3. `expirationSeconds`: 600 is the Kubernetes minimum and comfortably covers the boot-time window in which `bootstrap.py` reads the file (kubelet refreshes it continuously — observed rotation on the live minikube pod). Any reason to go longer? None found.
4. Does agent-sandbox's controller ever *reject* templates with ServiceAccount fields in future versions? v0.5.4 passes them through verbatim (§3); a pin bump must re-verify (per the existing pin-bump sweep rule).

**STOP — spike ends here per the task brief. No production code was changed. Awaiting G1.**
