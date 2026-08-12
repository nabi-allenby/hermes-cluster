# IdP support files

## `discord-openid-configuration.json`

Discord's own OIDC discovery document (`https://discord.com/.well-known/openid-configuration`)
plus the **one field Microsoft Entra External ID requires and Discord
omits**: `token_endpoint_auth_methods_supported`. Entra's custom-OIDC
validator rejects Discord's native URL with HTTP 400
("Required property 'token_endpoint_auth_methods_supported' not found");
pointing the IdP's `wellKnownEndpoint` at this file instead returns 201.
Entra explicitly permits a metadata URI on a different host than the
issuer, so this is supported configuration, not a validation bypass.

**This file is in the login trust path** — it defines the token and JWKS
endpoints Entra trusts for Discord federation. That is why it lives on
this repository's protected `main` branch: integrity = branch protection +
PR review; availability = GitHub's; independent of any cluster.

All endpoint values MUST be Discord's own (`https://discord.com/...`) —
review any change to this file with the same care as a key rotation.

When Discord adds the missing field upstream, re-point the Entra IdP's
`wellKnownEndpoint` to `https://discord.com/.well-known/openid-configuration`
and delete this directory.

Consumed by: `hermes-private-cluster` terraform (Entra custom OIDC IdP,
raw URL of this file).
