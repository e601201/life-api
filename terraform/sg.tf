# セキュリティグループはルールを別リソース（aws_vpc_security_group_*_rule）で持つ。
# aws_security_group の inline ブロックと違い、ルール 1 本が 1 リソースになるので
# 追加・削除の差分が読みやすく、ルール単位で import もできる。

# ---- api（ECS タスク）-------------------------------------------------------
# 手作業のときの life-api-sg はコンソールの CloudFormation スタックが持っているので、
# 名前を変えて作り直す（同じ VPC 内で SG 名は重複できない）。旧スタックの消し方は README。
resource "aws_security_group" "api" {
  name        = "life-api-ecs"
  description = "life-api ECS tasks"
  vpc_id      = data.aws_vpc.default.id
}

# 8080 への入口。ALB を立てるまでは許可した CIDR からタスクの IP を直接叩く。
resource "aws_vpc_security_group_ingress_rule" "api_http" {
  for_each = toset(var.api_allowed_cidrs)

  security_group_id = aws_security_group.api.id
  description       = "api (no ALB yet)"
  cidr_ipv4         = each.value
  ip_protocol       = "tcp"
  from_port         = 8080
  to_port           = 8080
}

# 外向きは全部通す。ECR の pull、SSM、CloudWatch Logs、RDS がここを通る
# （NAT も VPC エンドポイントも無いので、パブリック IP から直接出ていく）。
resource "aws_vpc_security_group_egress_rule" "api_all" {
  security_group_id = aws_security_group.api.id
  description       = "ECR pull, SSM, logs, RDS"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

# ---- db（RDS）----------------------------------------------------------------
# db_enabled が false でも残す（無料で、RDS を作り直すたびに参照が変わらない方がよい）。
resource "aws_security_group" "db" {
  name        = "life-api-rds"
  description = "life-api RDS PostgreSQL"
  vpc_id      = data.aws_vpc.default.id
}

# 5432 は api の SG からだけ。CIDR ではなく SG を参照しているので、タスクの IP が
# 変わっても効く。マイグレーションも同じ SG のタスクで流すので、自宅 IP を開ける
# 必要はない（09/05 は手元から流すために一時的に /32 を足していた）。
# 外向きのルールは無し。DB から出ていく通信は無い。
resource "aws_vpc_security_group_ingress_rule" "db_from_api" {
  security_group_id            = aws_security_group.db.id
  description                  = "PostgreSQL from api tasks"
  referenced_security_group_id = aws_security_group.api.id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}
