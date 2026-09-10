# Terraform 本体とプロバイダのバージョン。
#
# state は手元の terraform.tfstate に置く（.gitignore 済み）。state には RDS のパスワードが
# 平文で入るので、リポジトリに入れない。操作するのが 1 人・1 台のうちはこれで足りる。
# 複数人・複数台で触るようになったら S3 バックエンドに移す:
#
#   backend "s3" {
#     bucket       = "<state 用バケット>"
#     key          = "life-api/terraform.tfstate"
#     region       = "ap-northeast-1"
#     use_lockfile = true    # DynamoDB なしでロックできる（Terraform 1.10+）
#   }
terraform {
  required_version = ">= 1.7"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

# 認証情報はコードに書かない。AWS_PROFILE=life を環境変数で渡す（README 参照）。
provider "aws" {
  region = var.aws_region

  # ここで作る全リソースに付く。コンソールで「Terraform 管理かどうか」を見分けるため。
  default_tags {
    tags = {
      Project   = "life-api"
      ManagedBy = "terraform"
    }
  }
}
