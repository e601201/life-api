data "aws_caller_identity" "current" {}

locals {
  # ネットワークの参照はこの 2 つに寄せる。SG / ECS / RDS / ALB は vpc.tf の resource を直接見ない。
  # life#49 までは右辺がデフォルト VPC の data（aws_vpc.default / aws_subnets.default）で、
  # 独自 VPC に移すときはここの右辺を差し替えるだけで済んだ。
  # public_subnet_ids は for_each のキー順（a, c）で並ぶので、apply のたびに順番が変わることはない。
  vpc_id            = aws_vpc.life.id
  public_subnet_ids = [for s in aws_subnet.public : s.id]

  account_id = data.aws_caller_identity.current.account_id

  # タスク定義の secrets と、実行ロールの ssm:GetParameters の両方でこの名前を使う。
  database_url_param_name = "/life-api/database-url"
  database_url_param_arn  = "arn:aws:ssm:${var.aws_region}:${local.account_id}:parameter${local.database_url_param_name}"

  # JWT の署名鍵。database-url と同じ /life-api/ 配下なので、実行ロールの
  # ssm:GetParameters（parameter/life-api/*）はそのまま効く。
  jwt_secret_param_name = "/life-api/jwt-secret"
}
