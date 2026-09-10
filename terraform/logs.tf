# api と migrate の両方がここに書く（ストリームの prefix で分かれる）。
# 手作業のときは awslogs-create-group で自動生成していたが、保持期間を付けたいので
# Terraform で持つ。無期限にすると溜まる一方で、消す機会が来ない。
resource "aws_cloudwatch_log_group" "api" {
  name              = "/ecs/life-api"
  retention_in_days = var.log_retention_days
}
