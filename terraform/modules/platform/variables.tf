variable "agent_sandbox_version" {
  description = "kubernetes-sigs/agent-sandbox release tag (docs/p-m0.md is verified against this pin)"
  type        = string
  default     = "v0.5.4"
}

variable "namespace" {
  type    = string
  default = "hermes"
}

variable "release_name" {
  type    = string
  default = "hermes"
}

variable "chart" {
  description = "Chart ref: local path or OCI (oci://ghcr.io/nabi-allenby/hermes-cluster/hermes-platform)"
  type        = string
}

variable "discord_token" {
  description = "Discord bot token. Empty = connector runs without Discord."
  type        = string
  sensitive   = true
  default     = ""
}

variable "home_seed" {
  description = "First-boot HERMES_HOME files: keys .env, auth.json, config.yaml, SOUL.md"
  type        = map(string)
  sensitive   = true
}

variable "values" {
  description = "Chart value overrides as dotted-path map (e.g. {\"session.image\" = \"...\"})"
  type        = map(string)
  default     = {}
}

variable "values_yaml" {
  description = "Raw YAML values documents (for structured overrides like tolerations)"
  type        = list(string)
  default     = []
}
