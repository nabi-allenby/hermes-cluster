# aks module — everything with an Azure resource ID (design §9 invariant).
# The ~$25-36/mo standing deployment: AKS Free tier, one amd64 Spot node,
# single AZ (disks are zone-bound), Key Vault for the two platform secrets.

terraform {
  required_version = ">= 1.6"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

# Tenant comes from the caller's az login context — no variable to pass.
data "azurerm_client_config" "current" {}

resource "azurerm_resource_group" "this" {
  name     = var.resource_group_name
  location = var.location
}

resource "azurerm_virtual_network" "this" {
  name                = "${var.prefix}-vnet"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  address_space       = ["10.10.0.0/16"]
}

resource "azurerm_subnet" "nodes" {
  name                 = "nodes"
  resource_group_name  = azurerm_resource_group.this.name
  virtual_network_name = azurerm_virtual_network.this.name
  address_prefixes     = ["10.10.1.0/24"]
}

resource "azurerm_kubernetes_cluster" "this" {
  name                = "${var.prefix}-aks"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  dns_prefix          = var.prefix

  # Cost trap encoded: Free tier, explicitly. No SLA — acceptable for a
  # personal system whose workloads survive control-plane blips.
  sku_tier = "Free"

  # The system pool is the smallest thing AKS will run; agents land on the
  # Spot pool below.
  default_node_pool {
    name           = "system"
    vm_size        = var.system_vm_size
    node_count     = 1
    vnet_subnet_id = azurerm_subnet.nodes.id
    zones          = [var.zone]

    upgrade_settings {
      max_surge = "10%"
    }
  }

  identity {
    type = "SystemAssigned"
  }

  network_profile {
    # Azure CNI enforces NetworkPolicy (the chart ships policies default-on).
    network_plugin = "azure"
    network_policy = "azure"
  }

  oidc_issuer_enabled       = true
  workload_identity_enabled = true
}

# The agents' home: one amd64 Spot node. Spot eviction ≡ involuntary
# suspend by construction (P-AC5): the connector buffers, the controller
# reschedules, PVCs reattach.
resource "azurerm_kubernetes_cluster_node_pool" "spot" {
  name                  = "agents"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.this.id
  vm_size               = var.agent_vm_size
  vnet_subnet_id        = azurerm_subnet.nodes.id
  zones                 = [var.zone] # single AZ: disks are zone-bound

  priority        = "Spot"
  eviction_policy = "Delete"
  spot_max_price  = var.spot_max_price

  node_count = 1

  node_labels = {
    "hermes.nabi.dev/pool" = "agents"
  }
  node_taints = [
    "kubernetes.azure.com/scalesetpriority=spot:NoSchedule",
  ]
}

resource "azurerm_key_vault" "this" {
  name                       = "${var.prefix}-kv"
  location                   = azurerm_resource_group.this.location
  resource_group_name        = azurerm_resource_group.this.name
  tenant_id                  = data.azurerm_client_config.current.tenant_id
  sku_name                   = "standard"
  rbac_authorization_enabled = true
  purge_protection_enabled   = false
}
