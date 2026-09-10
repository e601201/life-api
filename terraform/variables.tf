variable "aws_region" {
  description = "リソースを置くリージョン"
  type        = string
  default     = "ap-northeast-1"
}

# ALB を立てるまで（W4）は、タスクのパブリック IP の 8080 を直接叩く。
# 誰でも叩ける状態にしないため、許可する CIDR を明示させる（既定は空 = 誰も繋げない）。
# 値は terraform.tfvars に書く（自宅 IP なのでコミットしない。terraform.tfvars.example 参照）。
variable "api_allowed_cidrs" {
  description = "api の 8080 に到達できる CIDR のリスト（例: 自宅 IP の /32）"
  type        = list(string)
  default     = []
}

# 既定 0 は「使わないときは止める」運用に合わせたもの。動かすときだけ
#   terraform apply -var api_desired_count=1
# で上げ、確認が済んだら引数なしの apply で 0 に戻す。
variable "api_desired_count" {
  description = "api サービスのタスク数"
  type        = number
  default     = 0
}

variable "api_image_tag" {
  description = "ECR から pull するイメージのタグ"
  type        = string
  default     = "latest"
}

# RDS は止めても 7 日で勝手に起き、停止中もストレージとバックアップの課金が続く。
# なので「使わない間は消す」。false にして apply すると DB と接続文字列（SSM）だけが消え、
# ECR / ECS / SG / IAM はそのまま残る。true に戻せば空の DB が作り直される。
variable "db_enabled" {
  description = "RDS インスタンスを作るか"
  type        = bool
  default     = true
}

# メジャーだけ指定する。マイナーは auto_minor_version_upgrade に任せ、
# 上がるたびに差分が出ないようにする（ローカルの compose は postgres:18-alpine）。
variable "db_engine_version" {
  description = "RDS PostgreSQL のバージョン（メジャーのみ）"
  type        = string
  default     = "18"
}

variable "db_instance_class" {
  description = "RDS のインスタンスクラス"
  type        = string
  default     = "db.t4g.micro"
}

variable "log_retention_days" {
  description = "CloudWatch Logs の保持日数"
  type        = number
  default     = 30
}
