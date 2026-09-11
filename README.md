# life-api

いま Markdown で書いている TIL / 日誌を、API として読み書きできるようにするサービス。
リソースは `entries`（TIL）/ `tags` / `users` の3つで、JWT 認証とタグ・全文・期間での検索を持つ。
Goa の design-first で DSL から OpenAPI を生成し、Go + ECS Fargate + RDS PostgreSQL 上で動かす。

## 開発

```
mise install                                    # Go のバージョンを揃える
goa gen github.com/e601201/life-api/design      # design.go から gen/ を生成
docker compose up -d db                         # DB だけ先に立ち上げる
export DATABASE_URL='postgres://life:life@localhost:5432/life?sslmode=disable'
export JWT_SECRET=$(openssl rand -base64 48)    # JWT の署名鍵（32 バイト以上）
go run ./cmd/life-migrate up                    # スキーマを適用する
go run ./cmd/life --host localhost              # http://localhost:8080 で起動
curl localhost:8080/health                      # => "server OK!"
```

api は起動時に DB へ繋ぎ、失敗したらそこで落ちる（最初のリクエストまで気づけないのを避けるため）。
接続先は `-db-url`、既定は環境変数 `DATABASE_URL`。全部コンテナで動かすなら後述の
`docker compose up` だけでよく、この手順は要らない。

JWT の署名鍵は環境変数 `JWT_SECRET` だけで受け取る（フラグにしないのは、コマンドラインに
載せると `ps` やシェルの履歴に残るため）。**リポジトリには置かない。** 32 バイト未満や未設定だと
起動時に落ちる。鍵を変えると発行済みのトークンは全て無効になる。ECS では SSM の
`/life-api/jwt-secret` から secrets として注入する（後述の Terraform）。

`--host container` を渡すと `0.0.0.0:8080` で listen する。コンテナや ECS ではこちらを使う
（`localhost` だと 127.0.0.1 に bind されるため、コンテナの外から繋がらない）。
bind するアドレスは `design/design.go` の `Server` / `Host` で定義しているので、
変えるときは `cmd/` ではなく DSL を直して `goa gen` し直す。

### docker compose（api + PostgreSQL）

api と PostgreSQL をまとめて立ち上げる。`db`（healthy になるまで待つ）→ `migrate`
（正常終了するまで待つ）→ `api` の順で起動する。

```sh
cp .env.example .env              # JWT_SECRET を書く（openssl rand -base64 48）。DB の設定は任意
docker compose up -d --build      # 起動（マイグレーションも流れる）
curl localhost:8080/health        # => "server OK!"
docker compose logs -f api        # ログ
docker compose down               # 停止（-v を付けると DB のデータも消える）
```

DB はホストの 5432 に出している。ローカルで別の Postgres が動いていると bind に失敗するので、
そのときは `POSTGRES_PORT` でずらす（コンテナ間は `db:5432` のままなので api には影響しない）。

```sh
psql -h 127.0.0.1 -p 5432 -U life -d life   # パスワードは life
```

api と migrate には接続先を `DATABASE_URL`（`postgres://life:life@db:5432/life?sslmode=disable`）
で渡している。compose.yaml では YAML のアンカーで 1 箇所に書き、両サービスで共有している。
api にはさらに `.env` の `JWT_SECRET` を渡す。compose.yaml に既定値は置いていないので、
`.env` に無いと api が起動直後に落ちる（`db` だけの起動は影響を受けない）。
この URL は素の文字列結合なので、`.env` でパスワードを変えるときに `@ : / ? #` を
含めるなら percent-encode すること（`p@ss` なら `p%40ss`）。
データは名前付きボリューム `pgdata` に残る（Postgres 18 で `PGDATA` の位置が変わったため、
マウント先は `/var/lib/postgresql/data` ではなく `/var/lib/postgresql`）。

### マイグレーション

[golang-migrate](https://github.com/golang-migrate/migrate) を使い、SQL は
`db/migrations/` に置いて `//go:embed` でバイナリに埋め込んでいる。実行時に .sql を
配る必要がないので、distroless のイメージにもバイナリ 1 つ置けば済む。

```sh
go run ./cmd/life-migrate up        # 未適用のものを全て適用する（何度流してもよい）
go run ./cmd/life-migrate down      # 直前の 1 つを巻き戻す
go run ./cmd/life-migrate down -n 3 # 3 つ巻き戻す
go run ./cmd/life-migrate version   # 適用済みのバージョンを表示する
```

適用は api とは別のコマンド（`cmd/life-migrate`）に分けてある。api の起動時に自動適用すると、
ECS でタスクが同時に複数立ち上がったときに同じマイグレーションを取り合うため。
ECS では同じイメージの `entryPoint` を `/life-migrate` に差し替えた単発タスクを流し、
成功してからサービスを新しいリビジョンに更新する。

新しいマイグレーションを足すときは、連番のペアを作る。

```
db/migrations/000002_create_tags.up.sql
db/migrations/000002_create_tags.down.sql
```

適用の途中で失敗すると `schema_migrations.dirty` が立ち、以降の up / down が全て弾かれる。
その場合は DB の状態を手で直してから、版を宣言し直すことになる。

### コード生成の注意

- **`goa gen` は何度実行してもよい。** `gen/` を作り直すだけで、手を入れたコードは壊さない
- **`goa example` は初回のみ。** `cmd/` とサービス実装（`health.go`）を生成するが、
  再実行すると**自分で書いた実装を雛形で上書きする**。
  API 名やメソッド名を変えたときは `goa gen` だけ流し、`cmd/` と実装は手で追随させる
- `gen/` はコミットする。Docker のビルドステージで goa CLI を入れずに済むため

### テスト

`entries` の CRUD と `users` は実際の PostgreSQL に対して流す。確かめたいものが SQL 側
（`date` へのキャスト、`RETURNING`、`pgx.ErrNoRows`、UNIQUE 制約違反、インデックスに合わせた並び）に
寄っているため、DB をモックすると肝心なところが残らない。`http_test.go` は Goa の生成コードごと
`httptest` で立てて、Authorization ヘッダ → 401 / `user_id` の流れを HTTP で確かめる。
JWT の発行・検証（`auth_test.go`）だけは DB を使わない。

接続先は `TEST_DATABASE_URL` で渡す。**テストは `entries` と `users` テーブルを空にする**ので、
開発用の DB とは別のデータベースを指すこと。`DATABASE_URL` ではなく専用の変数を
見ているのは、取り違えて開発中のデータを消さないため。

```sh
docker compose exec db psql -U life -d postgres -c 'CREATE DATABASE life_test'
TEST_DATABASE_URL='postgres://life:life@localhost:5432/life_test?sslmode=disable' go test ./...
```

`TEST_DATABASE_URL` が空のときは skip する（DB の無い環境でも `go test ./...` が
通るようにするため）。スキーマはテストの開始時に `db.Up` で適用するので、
マイグレーションを足したときも手当ては要らない。

### 認証（users と JWT）

`entries` は全メソッドで JWT を要求する。`POST /users` で登録し、`POST /users/login` で
JWT を受け取り、`Authorization: Bearer <token>` で渡す。トークンが無い・壊れている・期限切れは
401 `unauthorized`。有効期間は 24 時間で、失効の仕組みは無い（切れたらログインし直す）。

- 署名は HS256。鍵は `JWT_SECRET`（`auth.go`）。`alg` はトークン側に選ばせず、`iss` と `exp` も要求する
- パスワードは bcrypt で保存する。8 文字以上、72 バイト以内（bcrypt の上限）
- email は大文字小文字を区別しない（`lower(email)` の UNIQUE インデックス）。登録済みなら 409 `conflict`
- ログイン失敗は「email が無い」「パスワードが違う」を区別せず、同じ 401 を返す（email の列挙を防ぐ）
- `user_id` はリクエストでは受け取らず、作成時にトークンの `sub` から入れる。
  `entries` を `user_id` で絞る（自分のデータだけ見える）のは次の #43

DSL では `JWTSecurity("jwt")` を定義し、`entries` サービスと `users.me` に `Security(JWTAuth)` を
付けている。Token 属性は `Required` にしていない。必須にするとヘッダ無しが Goa のデコード段階で
400 になるため、空のまま `JWTAuth` まで通して 401 にしている。

### 打鍵確認（curl）

entries の CRUD は `entries` テーブルへの読み書き（`entries.go`）。プロセスを再起動しても
データは残る。一覧は記録日の新しい順（同じ日なら id の降順）で、**既定 10 件**を返す。
件数と位置は `limit`（1〜100）と `offset` でずらす。

`/health` はプロセスが生きているかに加えて DB への疎通も見る。繋がらないときは 503 を
返すので、ALB のターゲットグループから外れる（W4 でそこに繋ぐ）。

POST / PUT は `-H 'Content-Type: application/json'` が必須。
`-d` だけだと curl は form-urlencoded で送るため、Goa が 415 を返す。

```sh
# health
curl localhost:8080/health

# 登録（201）とログイン（200）。レスポンスは OAuth 2.0 のトークンレスポンスの形
curl -H 'Content-Type: application/json' localhost:8080/users \
  -d '{"email":"me@example.com","password":"correct horse"}'
TOKEN=$(curl -s -H 'Content-Type: application/json' localhost:8080/users/login \
  -d '{"email":"me@example.com","password":"correct horse"}' | jq -r .access_token)

# 自分の情報
curl -H "Authorization: Bearer $TOKEN" localhost:8080/users/me

# 作成（id・created_at・updated_at はサーバー側で付与。Location ヘッダに新リソースのパスが入る）
# user_id はリクエストでは受け取らず、トークンの sub から入る
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/entries \
  -d '{"title":"Goa入門","entry_date":"2026-08-31","kind":"til","body":"本文"}'

# 一覧（既定 10 件）/ 単体取得
curl -H "Authorization: Bearer $TOKEN" localhost:8080/entries
curl -H "Authorization: Bearer $TOKEN" 'localhost:8080/entries?limit=3&offset=10'   # 11 件目から 3 件
curl -H "Authorization: Bearer $TOKEN" localhost:8080/entries/1

# 更新（created_at と user_id は維持され、updated_at だけ進む。
#  body を省くと NULL に戻る＝PUT なので送った内容で全体を置き換える）
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/entries/1 \
  -d '{"title":"Goa入門(更新)","entry_date":"2026-08-31","kind":"til","body":"追記"}'

# 削除（204 No Content）
curl -i -X DELETE -H "Authorization: Bearer $TOKEN" localhost:8080/entries/1

# 異常系: トークン無し・不正なトークンは 401 unauthorized
curl -i localhost:8080/entries
curl -i -H 'Authorization: Bearer nope' localhost:8080/entries

# 異常系: ログイン失敗は 401（email の有無で文言を変えない）、登録済みの email は 409
curl -i -H 'Content-Type: application/json' localhost:8080/users/login \
  -d '{"email":"me@example.com","password":"wrong"}'
curl -i -H 'Content-Type: application/json' localhost:8080/users \
  -d '{"email":"ME@example.com","password":"another one"}'

# 異常系: 存在しない id は 404 not_found
curl -i -H "Authorization: Bearer $TOKEN" localhost:8080/entries/999

# 異常系: バリデーション違反は 400
#（kind は til | diary のみ、title / entry_date / kind は必須。
#  title の空文字と YYYY-MM-DD でない entry_date も 400 になる）
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/entries -d '{"title":"x"}'
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/entries \
  -d '{"title":"","entry_date":"banana","kind":"til"}'
```

## インフラ（Terraform）

AWS 側の構成は `terraform/` にある。W1〜W2 でコンソールと CLI で手作業したものを、
そのまま Terraform に書き起こした（life#40）。ALB と独自 VPC はまだ無い（W4）。
デフォルト VPC のパブリックサブネットに全部置き、api のタスクにはパブリック IP を付けて、
`terraform.tfvars` に書いた CIDR（自宅 IP）からだけ 8080 を叩ける。

```
[自宅 IP] --8080--> ECS Fargate (life-api) --5432--> RDS PostgreSQL (life-db)
                        ↑ pull                ↑ DATABASE_URL / JWT_SECRET
                       ECR              SSM Parameter Store
```

| ファイル | 中身 |
| --- | --- |
| `ecr.tf` | リポジトリ `life-api` と、新しい 10 世代だけ残すライフサイクル |
| `ecs.tf` | クラスタ `life`、タスク定義 `life-api` / `life-migrate`、サービス `life-api` |
| `rds.tf` / `ssm.tf` | RDS `life-db`（db.t4g.micro）と、SSM パラメータ `/life-api/database-url`（接続文字列）/ `/life-api/jwt-secret`（JWT の署名鍵。Terraform が生成し、api にだけ渡す） |
| `sg.tf` | SG `life-api-ecs`（8080 は許可 CIDR から）と `life-api-rds`（5432 は api の SG からだけ） |
| `iam.tf` | タスク実行ロール `ecsTaskExecutionRole`（ECR pull / ログ / SSM 読み取り） |
| `logs.tf` | ロググループ `/ecs/life-api`（30 日で消す） |

### 使い方

```sh
export AWS_PROFILE=life
cd terraform
cp terraform.tfvars.example terraform.tfvars   # 自宅 IP を書く（curl https://checkip.amazonaws.com）
terraform init
terraform plan
terraform apply                                 # RDS 込みで 5〜10 分かかる
```

state は手元の `terraform.tfstate`（gitignore 済み）。DB のパスワードが平文で入るので、
リポジトリにもイメージにも入れない（`.dockerignore` で `terraform/` ごと外している）。

### スキーマ適用と起動・停止

DB は空で作られるので、最初に `life-migrate up` を Fargate の単発タスクとして流す。
VPC 内から RDS に繋ぐので、09/05 のように自宅 IP を RDS の SG に開ける必要はない。

```sh
terraform output -raw migrate_command | sh      # 単発タスクを起動（終了コード 0 で成功）
aws logs tail /ecs/life-api --log-stream-name-prefix migrate --since 10m   # ログで確認

terraform apply -var api_desired_count=1        # api を 1 タスク起動
terraform apply                                 # 確認が済んだら 0 に戻す（既定値が 0）
terraform apply -var db_enabled=false           # RDS と接続文字列を消す（ECR / ECS / SG / IAM は残る）
```

**desired count は Terraform の変数で持つ**。CLI で 1 にしても、次の `terraform apply` で 0 に戻る。
「使い終わったら止める」を既定にするための挙動なので、そのまま使う。

タスクのパブリック IP は起動のたびに変わる。

```sh
task=$(aws ecs list-tasks --cluster life --service-name life-api --query 'taskArns[0]' --output text)
eni=$(aws ecs describe-tasks --cluster life --tasks "$task" \
  --query 'tasks[0].attachments[0].details[?name==`networkInterfaceId`].value' --output text)
aws ec2 describe-network-interfaces --network-interface-ids "$eni" \
  --query 'NetworkInterfaces[0].Association.PublicIp' --output text
```

### イメージの更新

`--platform linux/amd64` と `--provenance=false` の 2 つは省かない（08/29・09/05 の TIL）。
前者が無いと arm64 Mac のイメージになって Fargate で動かず、後者が無いとイメージが
OCI index になってタグなしの実体が並ぶ。

```sh
repo=$(terraform -chdir=terraform output -raw ecr_repository_url)
aws ecr get-login-password | docker login --username AWS --password-stdin "${repo%/*}"
docker build --platform linux/amd64 --provenance=false -t "$repo:latest" .
docker push "$repo:latest"
aws ecs update-service --cluster life --service life-api --force-new-deployment
```

### 手作業からの移行で決めたこと

- **import したもの**: ECR リポジトリ、ECS クラスタ、タスク実行ロール（管理ポリシーとインラインポリシー込み）、ロググループ。
  import ブロック（`import.tf`）で宣言して最初の apply で state に入れた。取り込みが済めば不要なので
  ファイルは消してある（残すと、環境を作り直したときに「無いものを import しようとして」落ちる）
- **作り直したもの**: サービスと SG。コンソールのウィザードが CloudFormation スタック
  `ECS-Console-V2-Service-life-api-service-0eeazhof-life-77a3fdf3` として作っていたため、
  import すると二重管理になる。Terraform では `life-api` / `life-api-ecs` と別名で作り、
  旧スタックは apply が通ったあとに `aws cloudformation delete-stack` で消した（サービスは desired 0 だったので何も止まらなかった）
- **新規に作るもの**: RDS、SSM パラメータ（接続文字列・JWT の署名鍵）、DB 用 SG。09/05 に削除済みなので import するものが無い
- **タスク定義は import しない**。リビジョンは不変なので、Terraform が新しいリビジョンを登録する。
  `life-api:1`〜`3` はそのまま残る（消したければ `deregister-task-definition`）
- **AWS Budgets（月 $20）は含めない**。アプリの構成ではなくアカウントの設定なので、このリポジトリの外

### 費用

| リソース | 目安 | 止め方 |
| --- | --- | --- |
| RDS db.t4g.micro + 20GB gp3 | ~$0.02/時（~$15/月） | `-var db_enabled=false` で消す |
| Fargate 0.25 vCPU / 0.5 GB | ~$0.02/時 | desired 0（既定） |
| ECR / ログ / SSM Standard | ほぼ 0 | - |

## 関連

- 目標・進め方・週次の計画は [life](https://github.com/e601201/life) リポジトリの `state/` に置いている
- 日々の進捗は life の `journal/YYYY/MM/DD.md` に日報として書く。このリポジトリにはコードのみ置く
