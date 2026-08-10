variable "prefix" {
  description = "Name prefix for all resources"
  type        = string
  default     = "hermes"
}

variable "resource_group_name" {
  type    = string
  default = "hermes-cluster"
}

variable "location" {
  type    = string
  default = "westeurope"
}

variable "zone" {
  description = "Single availability zone (disks are zone-bound; keep node + disks together)"
  type        = string
  default     = "1"
}

variable "system_vm_size" {
  type    = string
  default = "Standard_B2s"
}

variable "agent_vm_size" {
  description = "Spot VM for agent sessions. amd64 — the agent image must match."
  type        = string
  default     = "Standard_D2as_v5" # 2 vCPU / 8 GB
}

variable "spot_max_price" {
  description = "-1 = pay up to the on-demand price, never evicted on price"
  type        = number
  default     = -1
}

