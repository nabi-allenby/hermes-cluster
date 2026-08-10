# platform module — cloud-agnostic (design §9): agent-sandbox from the
# upstream release manifest, the namespace, the two out-of-band secrets, and
# the hermes-platform chart. Provider configs (cluster credentials) come from
# the calling example — TF outputs feed values, never the reverse.

terraform {
  required_version = ">= 1.6"
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.13"
    }
    kubectl = {
      source  = "gavinbunney/kubectl"
      version = "~> 1.14"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.4"
    }
  }
}

# agent-sandbox CRDs + controller, pinned. Upgrades happen by bumping
# var.agent_sandbox_version through its CI gate (docs/p-m0.md re-verify).
data "http" "agent_sandbox" {
  url = "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${var.agent_sandbox_version}/sandbox-with-extensions.yaml"
}

data "kubectl_file_documents" "agent_sandbox" {
  content = data.http.agent_sandbox.response_body
}

resource "kubectl_manifest" "agent_sandbox" {
  for_each          = data.kubectl_file_documents.agent_sandbox.manifests
  yaml_body         = each.value
  wait              = true
  server_side_apply = true
}

resource "kubernetes_namespace" "this" {
  metadata {
    name = var.namespace
  }
}

resource "kubernetes_secret" "discord_token" {
  count = var.discord_token == "" ? 0 : 1
  metadata {
    name      = "hrc-discord-token"
    namespace = kubernetes_namespace.this.metadata[0].name
  }
  data = {
    token = var.discord_token
  }
}

# First-boot HERMES_HOME seed: .env (LLM key), auth.json, config.yaml,
# SOUL.md. See hack/p-m1/make-seed.sh for the local equivalent.
resource "kubernetes_secret" "home_seed" {
  metadata {
    name      = "hermes-home-seed"
    namespace = kubernetes_namespace.this.metadata[0].name
  }
  data = var.home_seed
}

resource "helm_release" "platform" {
  name      = var.release_name
  namespace = kubernetes_namespace.this.metadata[0].name
  chart     = var.chart
  values    = var.values_yaml

  dynamic "set" {
    for_each = var.values
    content {
      name  = set.key
      value = set.value
    }
  }

  depends_on = [
    kubectl_manifest.agent_sandbox,
    kubernetes_secret.home_seed,
  ]
}
