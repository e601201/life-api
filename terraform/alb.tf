# ---- ALB ---------------------------------------------------------------------
# 外からの入口。タスクのパブリック IP は起動のたびに変わるので、人に渡せる固定の名前
# （ALB の DNS 名）が要る。HTTPS と独自ドメインは持たない（goals に無い）ので、
# リスナーは 80 の HTTP だけ。
#
# 立てている間は課金される（パブリック IPv4 2 個込みで ~$0.034/時、~$25/月）。desired 0 のときも
# ALB は残り、ターゲットが無い状態でヘルスチェックが空振りするだけなので、使わない間は消す。
# トグルの変数は付けず、terraform destroy -target=aws_lb.api で消す（README の #48 の節）。
resource "aws_lb" "api" {
  name               = "life-api"
  load_balancer_type = "application"
  internal           = false
  security_groups    = [aws_security_group.alb.id]
  subnets            = local.public_subnet_ids # 2 AZ 以上が必須
}

# タスクは awsvpc なので target_type は ip。ECS サービスが起動したタスクの ENI の IP を
# 登録し、止めたときに外す。手では登録しない。
resource "aws_lb_target_group" "api" {
  name        = "life-api"
  target_type = "ip"
  vpc_id      = local.vpc_id
  port        = 8080
  protocol    = "HTTP"

  # /health は DB への疎通も見る（health.go）。DB に繋がらないタスクは 503 で unhealthy に
  # なり、ECS が入れ替える。RDS を消している間は desired 0 なので、何も起きない。
  health_check {
    path                = "/health"
    matcher             = "200"
    interval            = 30
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 2
  }

  # desired 0 に戻すとき、既定の 300 秒はタスクの停止を待たせすぎる。
  # 接続を長く持つクライアントは無いので短くする。
  deregistration_delay = 30
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.api.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
}
