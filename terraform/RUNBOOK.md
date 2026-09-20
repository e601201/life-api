# terraform apply の手順（life-api）

2026-09-10 の #40 で実際に流した順番。すべて `~/workspace/life-api/terraform` で、
`export AWS_PROFILE=life` を先に通しておく。設計の意図は README の「インフラ（Terraform）」節。

## 1. 準備（1 回だけ）

```sh
cd ~/workspace/life-api/terraform
export AWS_PROFILE=life
terraform version                               # 1.16.2 であることを確認（mise.toml の pin）
cp terraform.tfvars.example terraform.tfvars    # 自宅 IP を api_allowed_cidrs に書く
terraform init                                  # provider（aws / random）を取ってくる
```

`terraform.tfvars` は life#48（ALB）で不要になった。今は `init` だけでよい。

## 2. 最初の apply（import と作成）

```sh
terraform plan                                  # 「7 to import, 13 to add, 4 to change, 0 to destroy」を確認
terraform apply                                 # yes で実行。RDS 作成込みで 5〜10 分
```

この段階で `import.tf` の import ブロックが効いて、ECR・クラスタ・実行ロール・ロググループが
state に入り、サービス・SG・RDS・SSM・タスク定義が新しく作られる。

## 3. スキーマ適用と動作確認

```sh
terraform output -raw migrate_command | sh      # life-migrate up を単発タスクで流す
aws logs tail /ecs/life-api --log-stream-name-prefix migrate --since 10m   # 成功をログで確認

terraform apply -var api_desired_count=1        # api を 1 タスク起動
# README の手順で公開 IP を引いて curl http://<IP>:8080/health
terraform apply                                 # 引数なしで desired 0 に戻す
```

## 4. 旧環境の片付けと差分ゼロの確認

```sh
aws cloudformation delete-stack \
  --stack-name ECS-Console-V2-Service-life-api-service-0eeazhof-life-77a3fdf3   # 旧サービス + 旧 SG
rm import.tf                                    # 取り込みが済んだので消す
terraform plan                                  # クラスタの幻の差分が出たので ecs.tf に configuration を追加
terraform plan                                  # No changes を確認
```

## 5. RDS を消す

```sh
terraform apply -var db_enabled=false           # destroy が RDS と SSM の 2 つだけであることを見て yes
```

## ALB を足したとき（life#48、2026-09-15）

plan は「9 to add, 2 to change, 1 to destroy」。サービスへの `load_balancer` の追加は作り直しではなく in-place だった。

```sh
terraform plan -out=/tmp/48.tfplan && terraform apply /tmp/48.tfplan   # 18:40 → 18:46（ALB 2 分、RDS 6 分）
terraform output -raw migrate_command | sh                             # 18:47、exit 0、migrated: version 4
aws ecs update-service --cluster life --service life-api --desired-count 1   # 18:48
aws ecs wait services-stable --cluster life --services life-api        # 47 秒で healthy
bash <打鍵スクリプト> "$(terraform output -raw api_url)"                # 18:49、health / users / entries / tags / 異常系 22 項目 ok
curl -m 5 http://<タスクのパブリック IP>:8080/health                    # タイムアウト（SG で遮断されている）
aws ecs update-service --cluster life --service life-api --desired-count 0   # 18:50
terraform plan                                                         # No changes
terraform plan -var db_enabled=false -out=/tmp/db.tfplan && terraform apply /tmp/db.tfplan   # destroy 2 件（RDS と database-url）
```

## 独自 VPC に移したとき（life#49、2026-09-16）

plan は「22 to add, 1 to change, 12 to destroy」。`vpc_id` を持つ SG とターゲットグループ（とその配下のルール・リスナー）は
provider が作り直しにするが、**DB のサブネットグループと ALB は「in-place 更新」で計画される**。どちらも AWS 側が
VPC をまたぐ変更を受け付けない（サブネットグループは "not in the same Vpc"、ALB はサブネット / SG の付け替え）ので、
`-replace` で作り直しを明示する。ALB が作り直しになるので DNS 名（`api_url`）は変わる。
ECS サービスはサブネット / SG / ターゲットグループの差し替えを in-place で受ける。RDS は消してある前提（W4 の運用どおり）。

```sh
terraform plan -replace=aws_lb.api -replace=aws_db_subnet_group.life -out=/tmp/49.tfplan
terraform apply /tmp/49.tfplan                                         # RDS 込みで 6〜10 分
terraform output -raw migrate_command | sh                             # 新しいサブネット / SG で単発タスク
aws logs tail /ecs/life-api --log-stream-name-prefix migrate --since 10m   # migrated: version 4
aws ecs update-service --cluster life --service life-api --desired-count 1
aws ecs wait services-stable --cluster life --services life-api
bash /tmp/life-api-48-check.sh "$(terraform output -raw api_url)"      # 09/15 と同じ 22 項目（URL は新しい ALB）
aws ecs update-service --cluster life --service life-api --desired-count 0
terraform plan                                                         # No changes
terraform plan -var db_enabled=false -out=/tmp/db.tfplan && terraform apply /tmp/db.tfplan   # destroy 2 件（RDS と database-url）
aws ec2 describe-security-groups --filters Name=vpc-id,Values=vpc-0f17b1e883bdd5f40 \
  --query 'SecurityGroups[].GroupName'                                 # デフォルト VPC に default だけ残っていること
```

## 実行ロールの名前を変えて通し確認したとき（life#51、2026-09-20）

plan は「10 to add, 0 to change, 5 to destroy」。前日に消した ALB / リスナー / ECS サービスと、RDS / 接続文字列が add。
実行ロールの名前を `ecsTaskExecutionRole` から `life-api-task-execution` に変えたので、ロールとポリシー 2 つ、
ロールの ARN を持つタスク定義 2 つが作り直しになる。

```sh
terraform plan -out=/tmp/51-up.tfplan && terraform apply /tmp/51-up.tfplan
# README「イメージの更新」で build / push（#50 のログの変更を ECR に載せる。ECR は既にあるので apply の前でも後でもよい）
terraform output -raw migrate_command | sh                             # 20:53、exit 0、migrated: version 4（新しいロールで pull / SSM / ログが通った）
aws ecs update-service --cluster life --service life-api --desired-count 1
aws ecs wait services-stable --cluster life --services life-api
bash ../scripts/smoke.sh "$(terraform output -raw api_url)"            # 20:55、34 項目 ok
# README「ログ」の start-query（life#50 で残していた確認）: user_id = 1 and status >= 400 で 404 / 409 の 3 行、
# request_id は ALB の Root=1-...。バリデーション違反の 400 には user_id が付かない（JWTAuth より先に弾かれる）
aws ecs update-service --cluster life --service life-api --desired-count 0
terraform plan -destroy -target=aws_lb.api -target=aws_db_instance.life -var db_enabled=false -out=/tmp/51-down.tfplan   # 5 to destroy
terraform apply /tmp/51-down.tfplan                                    # 21:31、5 destroyed（RDS は 1 分 51 秒、ALB は 17 秒）
```

削除は 3 回目で通った。1 回目は `AWS_PROFILE=life` を付け忘れ（No valid credential sources）、2 回目は計画ファイルが
削除を含まない計画で上書きされていて何も消えず（同じパスに `terraform plan -out=` を流すと上書きされる。apply の前に
`terraform show <計画ファイル>` で「5 to destroy」を確かめられる）、別名で作り直して通した。
mise が効いていないシェルでは terraform が 1.7.2 になり、1.16.2 で作った計画ファイルを読めない（`mise exec -- terraform`）。

## 次に AWS で動かすとき

1 と 4 は要らないので短くなる。

```sh
cd ~/workspace/life-api/terraform && export AWS_PROFILE=life
# コードを変えていれば、先に README の「イメージの更新」で build / push。
# migration はバイナリ埋め込みなので、イメージが古いと新しいスキーマも入らない（09/14）
terraform apply                                 # RDS と SSM、ALB・リスナー・ECS サービスが戻る（既定 db_enabled=true）
terraform output -raw migrate_command | sh      # 空の DB なのでスキーマ適用から
aws ecs update-service --cluster life --service life-api --desired-count 1   # 起動（state の desired は 0 のまま）
curl "$(terraform output -raw api_url)/health"  # ALB 経由。healthy になるまで 1 分ほど
aws ecs update-service --cluster life --service life-api --desired-count 0   # 確認後 0 に戻す
terraform destroy -target=aws_lb.api -target=aws_db_instance.life -var db_enabled=false   # 終わったら RDS と ALB を消す（5 to destroy）
```

打鍵は `bash ../scripts/smoke.sh "$(terraform output -raw api_url)"`（34 項目）でまとめて流せる。

ALB を毎回作り直すので、URL（`api_url`）は立てるたびに変わる。自宅の IP が変わっても直すものは無い。
