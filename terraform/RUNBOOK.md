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

## 次に AWS で動かすとき

1 と 4 は要らないので短くなる。

```sh
cd ~/workspace/life-api/terraform && export AWS_PROFILE=life
terraform apply                                 # RDS と SSM が戻る（既定 db_enabled=true）
terraform output -raw migrate_command | sh      # 空の DB なのでスキーマ適用から
terraform apply -var api_desired_count=1        # 起動
terraform apply                                 # 確認後 0 に戻す
terraform apply -var db_enabled=false           # 終わったら RDS を消す
```

自宅の IP が変わっていたら、起動の apply の前に `terraform.tfvars` を直す
（今の IP は `curl https://checkip.amazonaws.com`）。
