# イメージ置き場。push は手元から（README の「イメージの更新」参照）。
#
# force_delete は既定の false のまま。イメージが残っていると terraform destroy が
# ここで止まるが、消したいときは明示的に消す方が事故が少ない。
resource "aws_ecr_repository" "api" {
  name                 = "life-api"
  image_tag_mutability = "MUTABLE" # latest を上書きして使うため

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}

# 世代数で切る。「タグなしを N 日で消す」の定番ルールは使わない。
# Docker 29 は既定で provenance attestation を付けるため、イメージが OCI index になり、
# 本体と attestation が「タグなし」として並ぶ。untagged ルールはこれを消して
# latest の中身を失わせる（08/29 の TIL）。push 側で --provenance=false を付けているが、
# 忘れても壊れない側に倒しておく。
resource "aws_ecr_lifecycle_policy" "api" {
  repository = aws_ecr_repository.api.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "keep the newest 10 images regardless of tag"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = 10
        }
        action = {
          type = "expire"
        }
      }
    ]
  })
}
