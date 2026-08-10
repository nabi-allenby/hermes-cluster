output "kube_config" {
  value     = azurerm_kubernetes_cluster.this.kube_config[0]
  sensitive = true
}

output "cluster_name" {
  value = azurerm_kubernetes_cluster.this.name
}

output "resource_group_name" {
  value = azurerm_resource_group.this.name
}

output "key_vault_id" {
  value = azurerm_key_vault.this.id
}

output "oidc_issuer_url" {
  value = azurerm_kubernetes_cluster.this.oidc_issuer_url
}
