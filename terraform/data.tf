data "aws_caller_identity" "current" {}

# 独自 VPC に移る（life#49）までは、デフォルト VPC のパブリックサブネットに全部置く。
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
  # ネットワークの参照はこの 2 つに寄せる。SG / ECS / RDS / ALB は data を直接見ない。
  # 独自 VPC に移す（life#49）ときは、ここの右辺を resource に差し替えるだけで済ませる。
  # ALB は 2 つ以上の AZ にまたがるサブネットが要る（デフォルト VPC は 1a / 1c / 1d の 3 つ）。
  vpc_id            = data.aws_vpc.default.id
  public_subnet_ids = data.aws_subnets.default.ids

  account_id = data.aws_caller_identity.current.account_id

  # タスク定義の secrets と、実行ロールの ssm:GetParameters の両方でこの名前を使う。
  database_url_param_name = "/life-api/database-url"
  database_url_param_arn  = "arn:aws:ssm:${var.aws_region}:${local.account_id}:parameter${local.database_url_param_name}"

  # JWT の署名鍵。database-url と同じ /life-api/ 配下なので、実行ロールの
  # ssm:GetParameters（parameter/life-api/*）はそのまま効く。
  jwt_secret_param_name = "/life-api/jwt-secret"
}
