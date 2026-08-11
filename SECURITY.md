# Security

Report vulnerabilities privately via GitHub's **Report a vulnerability**
(Security → Advisories) on this repository — please do not open public
issues for exploitable findings.

Known trust-model limits are documented rather than hidden:

- Sessions in one release mutually trust each other's relay provisioning
  until [hermes-relay-connector#30](https://github.com/nabi-allenby/hermes-relay-connector/issues/30)
  lands — see the chart README's security notes.
- `/wake` is deliberately unauthenticated (idempotent, payload-free,
  cooldown-limited by the caller); the API surface that mutates state can
  be bearer-guarded (`HLM_API_TOKEN` / `lifecycleManager.apiTokenSecret`).
- Kubernetes Secrets are base64, not encryption — etcd encryption at rest
  is the cluster operator's responsibility.
