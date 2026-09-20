# タスク実行ロール。ECS エージェントがタスクを立ち上げるときに使う
# （ECR から pull、ログの書き込み、SSM から秘密を引く）。
# アプリ自身が AWS API を呼ぶわけではないので、タスクロールは付けていない。
#
# 名前は ecsTaskExecutionRole にしない。ECS のコンソールが自動で作るロールと同じ名前で、
# 既にあるアカウントでは apply が EntityAlreadyExists で落ちる（life#51 で変えた）。
resource "aws_iam_role" "task_execution" {
  name = "life-api-task-execution"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Service = "ecs-tasks.amazonaws.com"
        }
        Action = "sts:AssumeRole"
      }
    ]
  })
}

# ECR の pull と CloudWatch Logs への書き込み。AWS 管理ポリシー。
resource "aws_iam_role_policy_attachment" "task_execution" {
  role       = aws_iam_role.task_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# タスク定義の secrets（DATABASE_URL）を SSM から引く権限。管理ポリシーには含まれない。
# SecureString の復号は AWS 管理キー（aws/ssm）なので kms:Decrypt は要らない。
resource "aws_iam_role_policy" "task_execution_ssm" {
  name = "life-api-ssm-read"
  role = aws_iam_role.task_execution.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["ssm:GetParameters"]
        Resource = "arn:aws:ssm:${var.aws_region}:${local.account_id}:parameter/life-api/*"
      }
    ]
  })
}
