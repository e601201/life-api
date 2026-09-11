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

# JWT の署名鍵（HS256、api の環境変数 JWT_SECRET）。リポジトリにも tfvars にも置かず、
# DB のパスワードと同じく Terraform が生成して state と SSM にだけ持つ。
# RDS の有無（db_enabled）とは独立で、SSM Standard は無料なので常に作る。
#
# special=false でも 64 文字の英数字は 380 bit 相当で、HS256 が求める 256 bit を超える
# （api 側は 32 バイト未満を起動時に拒否する）。
# 値を変える（terraform taint / replace）と発行済みのトークンは全て無効になり、
# 全員がログインし直しになる。
resource "random_password" "jwt_secret" {
  length  = 64
  special = false
}

resource "aws_ssm_parameter" "jwt_secret" {
  name        = local.jwt_secret_param_name
  description = "life-api JWT_SECRET (managed by terraform)"
  type        = "SecureString"
  value       = random_password.jwt_secret.result
}
