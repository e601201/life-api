# ---- クラスタ ------------------------------------------------------------------
resource "aws_ecs_cluster" "life" {
  name = "life"

  setting {
    name  = "containerInsights"
    value = "disabled" # 有料。メトリクスが要るようになったら enabled に
  }

  # ECS Exec のログ先。DEFAULT は「タスク定義の awslogs に従う」で、API 上は未設定と同じ。
  # 書かないと、import したクラスタに対して provider がこのブロックを「消す」差分を出し続ける。
  configuration {
    execute_command_configuration {
      logging = "DEFAULT"
    }
  }
}

resource "aws_ecs_cluster_capacity_providers" "life" {
  cluster_name       = aws_ecs_cluster.life.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]
}

# ---- タスク定義 --------------------------------------------------------------
# api と migrate は同じイメージ。Dockerfile の ENTRYPOINT が /life、CMD が --host container
# なので、api はそのまま、migrate は entryPoint を /life-migrate に差し替える。
locals {
  image = "${aws_ecr_repository.api.repository_url}:${var.api_image_tag}"

  # DATABASE_URL と JWT_SECRET は環境変数ではなく secrets で渡す。タスク定義の JSON に
  # 平文が残らず、実行ロールが起動時に SSM から引いて注入する。
  database_url_secret = [
    {
      name      = "DATABASE_URL"
      valueFrom = local.database_url_param_arn
    }
  ]
  # 署名鍵は api だけが使う。migrate はスキーマを触るだけなので渡さない。
  jwt_secret = [
    {
      name      = "JWT_SECRET"
      valueFrom = aws_ssm_parameter.jwt_secret.arn
    }
  ]
}

resource "aws_ecs_task_definition" "api" {
  family                   = "life-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc" # Fargate はこれ固定
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.task_execution.arn

  # Dockerfile の GOARCH=amd64 / --platform linux/amd64 と揃える
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([
    {
      name      = "life-api"
      image     = local.image
      essential = true
      portMappings = [
        {
          name          = "http"
          containerPort = 8080
          protocol      = "tcp"
          appProtocol   = "http"
        }
      ]
      secrets = concat(local.database_url_secret, local.jwt_secret)
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.api.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "api"
        }
      }
    }
  ])
}

# スキーマ適用用。サービスにはせず、必要なときに run-task で 1 回流す
# （コマンドは outputs の migrate_command）。api の起動時に自動適用しないのは、
# タスクが同時に複数立ち上がったときに同じマイグレーションを取り合うため（README 参照）。
resource "aws_ecs_task_definition" "migrate" {
  family                   = "life-migrate"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.task_execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([
    {
      name       = "life-migrate"
      image      = local.image
      essential  = true
      entryPoint = ["/life-migrate"]
      command    = ["up"]
      secrets    = local.database_url_secret
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.api.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "migrate"
        }
      }
    }
  ])
}

# ---- サービス ----------------------------------------------------------------
resource "aws_ecs_service" "api" {
  name            = "life-api"
  cluster         = aws_ecs_cluster.life.id
  task_definition = aws_ecs_task_definition.api.arn
  desired_count   = var.api_desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = data.aws_subnets.default.ids
    security_groups = [aws_security_group.api.id]
    # NAT が無いので、ECR から pull するにも外から叩くにもパブリック IP が要る。
    # サブネット側の MapPublicIpOnLaunch とは別に、ここでも有効にしないと
    # CannotPullContainerError で起動と停止を繰り返す（08/29 の TIL）。
    assign_public_ip = true
  }

  # 新リビジョンのタスクが起動できないときに、旧リビジョンへ自動で戻す。
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }
  deployment_maximum_percent         = 200
  deployment_minimum_healthy_percent = 100

  enable_ecs_managed_tags = true

  depends_on = [aws_ecs_cluster_capacity_providers.life]
}
