# RDS PostgreSQL: encrypted, private, backed up with point-in-time recovery,
# protected from deletion, TLS required. RDS generates the master password and
# keeps it in Secrets Manager; it is never in this configuration or in a variable.

resource "aws_db_subnet_group" "this" {
  name       = "${local.name}-database"
  subnet_ids = var.private_subnet_ids
}

resource "aws_db_parameter_group" "this" {
  name        = "${local.name}-database"
  family      = var.database_parameter_group_family
  description = "Yumdrake PostgreSQL: TLS required"

  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }
}

resource "aws_db_instance" "this" {
  identifier = "${local.name}-database"

  engine         = "postgres"
  engine_version = var.database_engine_version
  instance_class = var.database_instance_class

  db_name  = var.database_name
  username = var.database_username

  # RDS creates the password and stores it in Secrets Manager.
  manage_master_user_password = true

  allocated_storage     = var.database_allocated_storage_gb
  max_allocated_storage = var.database_max_allocated_storage_gb
  storage_type          = "gp3"
  storage_encrypted     = true

  multi_az               = var.database_multi_az
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.database.id]
  publicly_accessible    = false
  parameter_group_name   = aws_db_parameter_group.this.name

  # Automated backups with a retention above zero are what enable point-in-time recovery.
  backup_retention_period   = var.database_backup_retention_days
  backup_window             = "02:00-03:00"
  maintenance_window        = "sun:03:30-sun:04:30"
  copy_tags_to_snapshot     = true
  deletion_protection       = true
  skip_final_snapshot       = false
  final_snapshot_identifier = "${local.name}-database-final"

  auto_minor_version_upgrade      = true
  enabled_cloudwatch_logs_exports = ["postgresql", "upgrade"]
}
