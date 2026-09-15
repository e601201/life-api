# ---- RDS PostgreSQL ----------------------------------------------------------
# db_enabled=false で apply すると、ここの aws_db_instance と ssm.tf の接続文字列だけ消える。
# サブネットグループと SG は無料なので残す（作り直したときに参照先が変わらない）。

resource "aws_db_subnet_group" "life" {
  name        = "life"
  description = "life-api"
  subnet_ids  = local.public_subnet_ids
}

# パスワードは Terraform が生成して state と SSM にだけ置く。リポジトリには入らない。
# special=false にしているのは、RDS が / @ " 空白 を拒否するのと、
# DATABASE_URL に埋めるときの percent-encode を考えなくて済むようにするため。
# 32 文字の英数字で 190 bit 相当あるので、記号無しでも強度は足りる。
resource "random_password" "db" {
  length  = 32
  special = false
}

resource "aws_db_instance" "life" {
  count = var.db_enabled ? 1 : 0

  identifier     = "life-db"
  engine         = "postgres"
  engine_version = var.db_engine_version
  instance_class = var.db_instance_class

  allocated_storage = 20 # gp3 の下限
  storage_type      = "gp3"
  storage_encrypted = true

  db_name  = "life"
  username = "life"
  password = random_password.db.result

  db_subnet_group_name   = aws_db_subnet_group.life.name
  vpc_security_group_ids = [aws_security_group.db.id]
  # 外からは繋がない。マイグレーションも VPC 内の ECS タスク（life-migrate）から流す。
  publicly_accessible = false

  # 学習用・単一 AZ。バックアップは 1 日（DB と同じ容量まで無料）。
  multi_az                = false
  backup_retention_period = 1
  copy_tags_to_snapshot   = true

  # 「使わない間は消す」運用なので、destroy を止めるものは付けない。
  # 消すときにスナップショットも残さない（残すと課金が続く）。
  deletion_protection = false
  skip_final_snapshot = true

  # 変更を次のメンテナンスウィンドウまで待たない。
  apply_immediately          = true
  auto_minor_version_upgrade = true

  performance_insights_enabled = false
  monitoring_interval          = 0
}
