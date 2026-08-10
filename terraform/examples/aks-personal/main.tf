# aks-personal — the ~$25-36/mo standing deployment (design §8).
#
#   terraform init
#   terraform apply \
#     -var tenant_id=<azure-tenant> \
#     -var discord_token=$(cat ~/.config/hrc/discord.token) \
#     -var seed_dir=~/my-hermes-seed
#
# The seed dir holds .env / auth.json / config.yaml / SOUL.md (LLM key lives
# in .env). The agent image must be pullable by the cluster and amd64 to
# match the Spot pool — see terraform/README.md.

terraform {
  required_version = ">= 1.6"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
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
  }
}

provider "azurerm" {
  features {}
}

module "aks" {
  source    = "../../modules/aks"
  tenant_id = var.tenant_id
}

provider "kubernetes" {
  host                   = module.aks.kube_config.host
  client_certificate     = base64decode(module.aks.kube_config.client_certificate)
  client_key             = base64decode(module.aks.kube_config.client_key)
  cluster_ca_certificate = base64decode(module.aks.kube_config.cluster_ca_certificate)
}

provider "helm" {
  kubernetes {
    host                   = module.aks.kube_config.host
    client_certificate     = base64decode(module.aks.kube_config.client_certificate)
    client_key             = base64decode(module.aks.kube_config.client_key)
    cluster_ca_certificate = base64decode(module.aks.kube_config.cluster_ca_certificate)
  }
}

provider "kubectl" {
  host                   = module.aks.kube_config.host
  client_certificate     = base64decode(module.aks.kube_config.client_certificate)
  client_key             = base64decode(module.aks.kube_config.client_key)
  cluster_ca_certificate = base64decode(module.aks.kube_config.cluster_ca_certificate)
  load_config_file       = false
}

module "platform" {
  source = "../../modules/platform"

  chart         = var.chart
  discord_token = var.discord_token
  home_seed = {
    ".env"        = file("${var.seed_dir}/.env")
    "auth.json"   = file("${var.seed_dir}/auth.json")
    "config.yaml" = file("${var.seed_dir}/config.yaml")
    "SOUL.md"     = file("${var.seed_dir}/SOUL.md")
  }
  values = merge(
    {
      "session.image"                 = var.agent_image
      "connector.buffer.storageClass" = "managed-csi"
      "session.home.storageClass"     = "managed-csi"
    },
    var.extra_values,
  )
  # Sessions land on the tainted Spot pool; connector + LM stay on system.
  values_yaml = [<<-YAML
    session:
      nodeSelector:
        hermes.nabi.dev/pool: agents
      tolerations:
        - key: kubernetes.azure.com/scalesetpriority
          operator: Equal
          value: spot
          effect: NoSchedule
  YAML
  ]
}

variable "tenant_id" { type = string }

variable "discord_token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "seed_dir" {
  description = "Directory with .env, auth.json, config.yaml, SOUL.md"
  type        = string
}

variable "chart" {
  type    = string
  default = "oci://ghcr.io/nabi-allenby/hermes-cluster/hermes-platform"
}

variable "agent_image" {
  description = "Pullable amd64 hermes-agent image (no official image exists; build your own)"
  type        = string
}

variable "extra_values" {
  type    = map(string)
  default = {}
}

output "cluster_name" { value = module.aks.cluster_name }
output "resource_group" { value = module.aks.resource_group_name }
