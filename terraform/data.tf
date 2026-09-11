data "aws_caller_identity" "current" {}

# W4 で独自 VPC に移るまでは、デフォルト VPC のパブリックサブネットに全部置く。
# 自前で作らず参照だけにしているのは、ここで時間を溶かさない方針（goals.md）のため。
#
# デフォルト VPC は削除できるし、削除されたまま気づかないことがある（08/29 に実際に無かった）。
# 見つからずにここで落ちたら `aws ec2 create-default-vpc` で一式復元する。無料。
data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

locals {
  account_id = data.aws_caller_identity.current.account_id

  # タスク定義の secrets と、実行ロールの ssm:GetParameters の両方でこの名前を使う。
  database_url_param_name = "/life-api/database-url"
  database_url_param_arn  = "arn:aws:ssm:${var.aws_region}:${local.account_id}:parameter${local.database_url_param_name}"

  # JWT の署名鍵。database-url と同じ /life-api/ 配下なので、実行ロールの
  # ssm:GetParameters（parameter/life-api/*）はそのまま効く。
  jwt_secret_param_name = "/life-api/jwt-secret"
}
