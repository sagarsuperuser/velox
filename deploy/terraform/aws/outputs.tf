output "ec2_public_ip" {
  description = "Public IPv4 address of the velox-api EC2 host."
  value       = aws_instance.velox.public_ip
}

output "ec2_public_dns" {
  description = "Public DNS name of the velox-api EC2 host."
  value       = aws_instance.velox.public_dns
}

output "rds_endpoint" {
  description = "RDS Postgres endpoint (host:port). Use as DATABASE_URL host."
  value       = aws_db_instance.this.endpoint
}

output "rds_address" {
  description = "RDS Postgres host (no port)."
  value       = aws_db_instance.this.address
}

output "rds_db_name" {
  description = "RDS database name."
  value       = aws_db_instance.this.db_name
}

output "s3_backup_bucket" {
  description = "S3 bucket for pg_basebackup + WAL archive."
  value       = aws_s3_bucket.backups.bucket
}

output "s3_backup_bucket_arn" {
  description = "ARN of the S3 backup bucket — useful for IAM policies in adjacent stacks."
  value       = aws_s3_bucket.backups.arn
}

output "vpc_id" {
  description = "VPC ID — useful when peering or attaching extra resources."
  value       = aws_vpc.this.id
}

output "ssh_command" {
  description = "Convenience SSH command (assumes the key is at ~/.ssh/<key_name>.pem)."
  value       = "ssh -i ~/.ssh/${var.key_name}.pem ec2-user@${aws_instance.velox.public_ip}"
}

output "next_steps" {
  description = "Operator hint: how to retrieve the auto-generated bootstrap token from cloud-init logs."
  value       = "ssh into the host (see ssh_command output), then: sudo grep 'Bootstrap:' /var/log/velox-bootstrap.log"
}
