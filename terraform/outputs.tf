output "ecr_repository_url" {
  description = "docker push 先"
  value       = aws_ecr_repository.api.repository_url
}

output "ecs_cluster_name" {
  value = aws_ecs_cluster.life.name
}

output "ecs_service_name" {
  value = aws_ecs_service.api.name
}

output "db_endpoint" {
  description = "RDS のエンドポイント（host:port）。db_enabled=false のときは null"
  value       = var.db_enabled ? aws_db_instance.life[0].endpoint : null
}

# スキーマ適用を 1 回流すコマンド。VPC 内の Fargate タスクから RDS に繋ぐので、
# 自宅 IP を RDS の SG に開ける必要がない。
#   terraform output -raw migrate_command | sh
output "migrate_command" {
  description = "life-migrate up を Fargate の単発タスクとして流す"
  value = format(
    "aws ecs run-task --cluster %s --launch-type FARGATE --task-definition %s --network-configuration 'awsvpcConfiguration={subnets=[%s],securityGroups=[%s],assignPublicIp=ENABLED}'",
    aws_ecs_cluster.life.name,
    aws_ecs_task_definition.migrate.family,
    join(",", data.aws_subnets.default.ids),
    aws_security_group.api.id,
  )
}
