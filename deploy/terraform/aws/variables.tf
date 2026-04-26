################################################################
# Required inputs
################################################################

variable "region" {
  description = "AWS region (e.g. us-east-1, eu-west-1, ap-south-1)."
  type        = string
}

variable "db_password" {
  description = "Master password for the RDS Postgres instance. Sensitive."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.db_password) >= 16
    error_message = "db_password must be at least 16 characters."
  }
}

variable "key_name" {
  description = "Name of an existing AWS EC2 key pair (uploaded out-of-band) used for SSH into the velox host. Create with: aws ec2 create-key-pair --key-name velox-dev --query 'KeyMaterial' --output text > velox-dev.pem"
  type        = string
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC. Pick a /16 that doesn't collide with anything you peer with."
  type        = string
  default     = "10.30.0.0/16"

  validation {
    condition     = can(cidrnetmask(var.vpc_cidr))
    error_message = "vpc_cidr must be a valid CIDR block."
  }
}

################################################################
# Sizing — defaults to a development-cost tier (~$30-50/mo).
# v1 = single-AZ, single-EC2, single-RDS-instance — boring + cheap.
################################################################

variable "ec2_instance_type" {
  description = "EC2 instance type for the velox-api host. t3.small (2 vCPU / 2 GB RAM) handles ~1k events/sec."
  type        = string
  default     = "t3.small"
}

variable "ec2_root_volume_size_gb" {
  description = "EBS root volume size for the velox-api host."
  type        = number
  default     = 30
}

variable "rds_instance_class" {
  description = "RDS Postgres instance class. db.t3.small (2 vCPU / 2 GB RAM) is fine for early production."
  type        = string
  default     = "db.t3.small"
}

variable "rds_allocated_storage_gb" {
  description = "RDS allocated storage in GB."
  type        = number
  default     = 20
}

variable "rds_max_allocated_storage_gb" {
  description = "RDS storage autoscaling cap. Leave equal to allocated to disable autoscaling."
  type        = number
  default     = 100
}

variable "rds_backup_retention_days" {
  description = "Days of automated RDS backups to keep. Set to 0 to disable (not recommended)."
  type        = number
  default     = 7
}

variable "postgres_engine_version" {
  description = "RDS Postgres major.minor version. Velox is tested against 16.x."
  type        = string
  default     = "16.4"
}

################################################################
# Network access — keep tight; this is open-by-default territory.
################################################################

variable "ssh_allowed_cidrs" {
  description = "CIDR blocks allowed to SSH into the velox host. Default is a placeholder; OVERRIDE before applying. RFC1918 ranges only is the safest posture (use SSM Session Manager for production)."
  type        = list(string)
  default     = ["203.0.113.0/24"] # documentation-only range; replace before apply
}

variable "http_allowed_cidrs" {
  description = "CIDR blocks allowed to reach the velox HTTP port (80). Default is open since the API is the public surface."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

################################################################
# Velox application config — passed to user-data and rendered into
# the .env file the Compose stack reads.
################################################################

variable "velox_image_tag" {
  description = "Container image tag to deploy. Pin in production."
  type        = string
  default     = "0.1.0"
}

variable "velox_repo_url" {
  description = "Git URL to clone the velox repo from on the host (so user-data can run docker compose against deploy/compose)."
  type        = string
  default     = "https://github.com/sagarsuperuser/velox.git"
}

variable "velox_repo_ref" {
  description = "Git ref (tag/branch/SHA) to check out."
  type        = string
  default     = "main"
}

variable "velox_app_env" {
  description = "APP_ENV value (local | staging | production)."
  type        = string
  default     = "production"

  validation {
    condition     = contains(["local", "staging", "production"], var.velox_app_env)
    error_message = "velox_app_env must be one of: local, staging, production."
  }
}

################################################################
# Tagging / naming
################################################################

variable "name_prefix" {
  description = "Prefix prepended to every resource name."
  type        = string
  default     = "velox"
}

variable "environment" {
  description = "Environment label applied to default tags."
  type        = string
  default     = "dev"
}

variable "tags" {
  description = "Additional tags merged into every resource."
  type        = map(string)
  default     = {}
}
