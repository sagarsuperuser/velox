# Velox single-VPC + single-EC2 + RDS + S3 module.
#
# Architecture decision (locked): NOT EKS, NOT autoscaling, NOT
# multi-AZ. v1 = boring + cheap. The Helm chart + EKS path is for
# users who already operate K8s; this module is the single-VM path
# operators reach for first.
#
#   +-----------------------------------------------------+
#   |                          VPC                        |
#   |                                                     |
#   |   public subnet (AZ-A)        public subnet (AZ-B)  |
#   |        +-------+                                    |
#   |        |  EC2  |                                    |
#   |        +---+---+                                    |
#   |            |                                        |
#   |   private subnet (AZ-A)       private subnet (AZ-B) |
#   |             \                       /               |
#   |              +---- RDS Postgres ----+               |
#   |             (single-AZ; multi-AZ is a v2 follow-up) |
#   +-----------------------------------------------------+
#                |
#                v
#         S3 backup bucket (versioned, SSE, BPA)
#
# Two AZs are used for the RDS DB subnet group only (RDS demands ≥2
# subnets in distinct AZs even for single-AZ instances). The EC2
# host lives in a single public subnet; the RDS instance lives in
# the private subnet group.

locals {
  name = var.name_prefix
}

data "aws_availability_zones" "available" {
  state = "available"
}

################################################################
# VPC, subnets, IGW, route tables
################################################################

resource "aws_vpc" "this" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = {
    Name = "${local.name}-vpc"
  }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id

  tags = {
    Name = "${local.name}-igw"
  }
}

# Two public subnets in two AZs. EC2 sits in public_a; public_b
# exists only because RDS DB subnet groups demand ≥2 AZs.
resource "aws_subnet" "public" {
  count                   = 2
  vpc_id                  = aws_vpc.this.id
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, count.index)
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  map_public_ip_on_launch = true

  tags = {
    Name = "${local.name}-public-${data.aws_availability_zones.available.names[count.index]}"
  }
}

# Two private subnets in the same two AZs for the RDS DB subnet group.
resource "aws_subnet" "private" {
  count             = 2
  vpc_id            = aws_vpc.this.id
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, count.index + 10)
  availability_zone = data.aws_availability_zones.available.names[count.index]

  tags = {
    Name = "${local.name}-private-${data.aws_availability_zones.available.names[count.index]}"
  }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }

  tags = {
    Name = "${local.name}-public-rt"
  }
}

resource "aws_route_table_association" "public" {
  count          = 2
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# Private subnets need a route table even if it has no IGW route;
# leaving it default-VPC-attached works, but an explicit one is
# clearer.
resource "aws_route_table" "private" {
  vpc_id = aws_vpc.this.id

  tags = {
    Name = "${local.name}-private-rt"
  }
}

resource "aws_route_table_association" "private" {
  count          = 2
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

################################################################
# Security groups
################################################################

resource "aws_security_group" "ec2" {
  name        = "${local.name}-ec2"
  description = "Velox API host: HTTP in, SSH from operator, all egress."
  vpc_id      = aws_vpc.this.id

  ingress {
    description = "HTTP from public"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = var.http_allowed_cidrs
  }

  ingress {
    description = "SSH from operator"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = var.ssh_allowed_cidrs
  }

  egress {
    description = "All egress"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${local.name}-ec2-sg"
  }
}

resource "aws_security_group" "rds" {
  name        = "${local.name}-rds"
  description = "Velox RDS Postgres: 5432 from the EC2 SG only."
  vpc_id      = aws_vpc.this.id

  ingress {
    description     = "Postgres from velox EC2 host"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.ec2.id]
  }

  egress {
    description = "All egress"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${local.name}-rds-sg"
  }
}

################################################################
# RDS Postgres
################################################################

resource "aws_db_subnet_group" "this" {
  name       = "${local.name}-db-subnets"
  subnet_ids = aws_subnet.private[*].id

  tags = {
    Name = "${local.name}-db-subnets"
  }
}

resource "aws_db_parameter_group" "this" {
  name   = "${local.name}-pg16"
  family = "postgres16"

  # Velox is fine with default Postgres parameters; this group exists
  # so future tuning lands here without recreating the DB instance.
  parameter {
    name  = "log_statement"
    value = "ddl"
  }
}

resource "aws_db_instance" "this" {
  identifier     = "${local.name}-db"
  engine         = "postgres"
  engine_version = var.postgres_engine_version
  instance_class = var.rds_instance_class

  allocated_storage     = var.rds_allocated_storage_gb
  max_allocated_storage = var.rds_max_allocated_storage_gb
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name  = "velox"
  username = "velox"
  password = var.db_password
  port     = 5432

  vpc_security_group_ids = [aws_security_group.rds.id]
  db_subnet_group_name   = aws_db_subnet_group.this.name
  parameter_group_name   = aws_db_parameter_group.this.name

  publicly_accessible        = false
  multi_az                   = false # v2 follow-up
  backup_retention_period    = var.rds_backup_retention_days
  delete_automated_backups   = true
  copy_tags_to_snapshot      = true
  skip_final_snapshot        = true # OK for dev; flip in prod
  apply_immediately          = true
  auto_minor_version_upgrade = true

  tags = {
    Name = "${local.name}-db"
  }
}

################################################################
# S3 backup bucket
################################################################

resource "random_id" "bucket_suffix" {
  byte_length = 4
}

resource "aws_s3_bucket" "backups" {
  bucket = "${local.name}-backups-${random_id.bucket_suffix.hex}"

  tags = {
    Name    = "${local.name}-backups"
    Purpose = "velox-pg-basebackup-and-wal"
  }
}

resource "aws_s3_bucket_versioning" "backups" {
  bucket = aws_s3_bucket.backups.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket                  = aws_s3_bucket.backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    id     = "tier-old-base-backups"
    status = "Enabled"
    filter {
      prefix = "base/"
    }
    transition {
      days          = 30
      storage_class = "GLACIER_IR"
    }
    transition {
      days          = 90
      storage_class = "DEEP_ARCHIVE"
    }
    expiration {
      days = 365
    }
  }

  rule {
    id     = "expire-old-wal"
    status = "Enabled"
    filter {
      prefix = "wal/"
    }
    expiration {
      days = 14
    }
  }
}

################################################################
# IAM — instance profile that lets EC2 write to the backup bucket
################################################################

data "aws_iam_policy_document" "ec2_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ec2" {
  name               = "${local.name}-ec2"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume_role.json

  tags = {
    Name = "${local.name}-ec2"
  }
}

data "aws_iam_policy_document" "ec2_inline" {
  statement {
    sid       = "ListBucket"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.backups.arn]
  }
  statement {
    sid       = "WriteBackups"
    actions   = ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"]
    resources = ["${aws_s3_bucket.backups.arn}/*"]
  }
}

resource "aws_iam_role_policy" "ec2_backups" {
  name   = "${local.name}-backups"
  role   = aws_iam_role.ec2.id
  policy = data.aws_iam_policy_document.ec2_inline.json
}

# SSM agent perms — gives operators a no-SSH-key fallback (Session
# Manager) for emergencies. Cheap insurance.
resource "aws_iam_role_policy_attachment" "ec2_ssm" {
  role       = aws_iam_role.ec2.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "ec2" {
  name = "${local.name}-ec2"
  role = aws_iam_role.ec2.name
}

################################################################
# EC2 host — Amazon Linux 2023, t3.small by default
################################################################

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-x86_64"]
  }
  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

resource "aws_instance" "velox" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.ec2_instance_type
  key_name               = var.key_name
  subnet_id              = aws_subnet.public[0].id
  vpc_security_group_ids = [aws_security_group.ec2.id]
  iam_instance_profile   = aws_iam_instance_profile.ec2.name

  associate_public_ip_address = true

  user_data_replace_on_change = true
  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    velox_repo_url   = var.velox_repo_url
    velox_repo_ref   = var.velox_repo_ref
    velox_image_tag  = var.velox_image_tag
    velox_app_env    = var.velox_app_env
    db_password      = var.db_password
    db_user          = aws_db_instance.this.username
    db_name          = aws_db_instance.this.db_name
    rds_endpoint     = aws_db_instance.this.endpoint
    s3_backup_bucket = aws_s3_bucket.backups.bucket
  })

  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.ec2_root_volume_size_gb
    encrypted             = true
    delete_on_termination = true
  }

  metadata_options {
    http_tokens                 = "required" # IMDSv2 only
    http_put_response_hop_limit = 2
  }

  tags = {
    Name = "${local.name}-api"
  }

  # The EC2 host depends on RDS being up so user-data can write the
  # DATABASE_URL pointing at the live endpoint.
  depends_on = [aws_db_instance.this]
}
