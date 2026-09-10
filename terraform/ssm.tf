# api / migrate が読む接続文字列。タスク定義の secrets からこの ARN を参照する。
#
# PostgreSQL 15 以降の RDS は既定で rds.force_ssl=1 なので sslmode=require が要る。
# ローカルの sslmode=disable をそのまま持っていくと、SG が閉じているのと同じ症状
# （繋がらない）で拒否される（09/05 の TIL）。
#
# Standard の SecureString は無料（Secrets Manager は 1 件 $0.40/月）。
# 暗号鍵は AWS 管理の aws/ssm なので、実行ロールに kms:Decrypt は要らない。
resource "aws_ssm_parameter" "database_url" {
  count = var.db_enabled ? 1 : 0

  name        = local.database_url_param_name
  description = "life-api DATABASE_URL (managed by terraform)"
  type        = "SecureString"
  value = format(
    "postgres://%s:%s@%s/%s?sslmode=require",
    aws_db_instance.life[0].username,
    urlencode(random_password.db.result),
    aws_db_instance.life[0].endpoint, # host:port
    aws_db_instance.life[0].db_name,
  )
}
