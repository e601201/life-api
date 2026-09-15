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

## 次に AWS で動かすとき

1 と 4 は要らないので短くなる。

```sh
cd ~/workspace/life-api/terraform && export AWS_PROFILE=life
# コードを変えていれば、先に README の「イメージの更新」で build / push。
# migration はバイナリ埋め込みなので、イメージが古いと新しいスキーマも入らない（09/14）
terraform apply                                 # RDS と SSM が戻る（既定 db_enabled=true）
terraform output -raw migrate_command | sh      # 空の DB なのでスキーマ適用から
aws ecs update-service --cluster life --service life-api --desired-count 1   # 起動（state の desired は 0 のまま）
curl "$(terraform output -raw api_url)/health"  # ALB 経由。healthy になるまで 1 分ほど
aws ecs update-service --cluster life --service life-api --desired-count 0   # 確認後 0 に戻す
terraform apply -var db_enabled=false           # 終わったら RDS を消す
```

URL は ALB の DNS 名で固定なので、自宅の IP が変わっても直すものは無い。
