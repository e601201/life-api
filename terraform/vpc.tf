# ---- VPC ---------------------------------------------------------------------
# W1 で意図的に飛ばしてデフォルト VPC を参照していた分を、ALB が乗ったあとで自前にする（life#49）。
# 中身はデフォルト VPC と同じ「パブリックサブネットだけ」の構成。プライベートサブネットも
# NAT ゲートウェイも VPC エンドポイントも持たない。
#
# タスクをプライベートサブネットに置くと、ECR の pull / SSM / CloudWatch Logs への出口として
# NAT ゲートウェイ（~$0.062/時 + 転送量）か VPC エンドポイント（ecr.api / ecr.dkr / ssm / logs の 4 つ。
# 1 つ ~$0.014/時 × AZ 数で、1 AZ に絞っても ~$0.056/時）が要り、どちらも ALB（~$0.03/時）より高い。
# 入口は SG で ALB だけに絞れている（sg.tf）ので、タスクにパブリック IP が付いていても直接は叩けない。
# 学習目的とコストの釣り合いで、ここは持たない。
resource "aws_vpc" "life" {
  cidr_block = "10.0.0.0/16" # デフォルト VPC は 172.31.0.0/16。重ならない側に置く

  # デフォルト VPC と同じにしておく。DNS 解決は ECR / SSM / RDS のエンドポイント名を
  # VPC 内から引くのに要る。hostnames は今の構成では使わないが、既定の false にすると
  # デフォルト VPC からの移行で挙動が変わる箇所が増えるので揃える。
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = "life" }
}

# ALB は 2 つ以上の AZ にまたがるサブネットが要る。1a と 1c の 2 つ（デフォルト VPC は 1a / 1c / 1d の 3 つだった）。
# キーは AZ の末尾 1 文字。アドレスが aws_subnet.public["a"] の形になり、順番を入れ替えても作り直しにならない。
locals {
  public_subnets = {
    a = { az = "${var.aws_region}a", cidr = "10.0.0.0/24" }
    c = { az = "${var.aws_region}c", cidr = "10.0.1.0/24" }
  }
}

resource "aws_subnet" "public" {
  for_each = local.public_subnets

  vpc_id            = aws_vpc.life.id
  availability_zone = each.value.az
  cidr_block        = each.value.cidr

  # Fargate のタスクにパブリック IP が付くかはサービス側の assign_public_ip で決まり、この属性は見ない
  # （ecs.tf）。「このサブネットはパブリック」の印として、デフォルト VPC と同じ true にしておく。
  map_public_ip_on_launch = true

  tags = { Name = "life-public-${each.key}" }
}

resource "aws_internet_gateway" "life" {
  vpc_id = aws_vpc.life.id

  tags = { Name = "life" }
}

# 「パブリックサブネット」の実体はこれ。0.0.0.0/0 を IGW に向けたルートテーブルを
# 両方のサブネットに関連付ける。VPC 内（10.0.0.0/16）の local ルートは自動で付く。
resource "aws_route_table" "public" {
  vpc_id = aws_vpc.life.id

  tags = { Name = "life-public" }
}

resource "aws_route" "public_internet" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.life.id
}

resource "aws_route_table_association" "public" {
  for_each = aws_subnet.public

  subnet_id      = each.value.id
  route_table_id = aws_route_table.public.id
}
