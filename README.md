# life-api

いま Markdown で書いている TIL / 日誌を、API として読み書きできるようにするサービス。
リソースは `entries`（TIL）/ `tags` / `users` の3つで、JWT 認証とタグ・部分一致・期間での検索を持つ。
Goa の design-first で DSL から OpenAPI を生成し、Go + ECS Fargate + RDS PostgreSQL 上で動かす。

## 構成

```
[誰でも] --80--> ALB (life-api) --8080--> ECS Fargate (life-api) --5432--> RDS PostgreSQL (life-db)
                                              ↑ pull                ↑ DATABASE_URL / JWT_SECRET
                                             ECR              SSM Parameter Store
```

- **Go + Goa**: 入社先（フラー）と同じ言語とフレームワーク
- **ALB + ECS Fargate + RDS PostgreSQL + ECR**: 日本の企業で最もよく使われる構成で、実務への直結度が最も高い。
  GCP + Kubernetes の経験がそのまま転用できる（Pod → Task、Deployment → Service、Ingress → ALB + ターゲットグループ）

個々の判断の理由は「インフラ（Terraform）」の各「判断」の節と、「認証」「tags」「検索」の節に書いてある。

## 開発

```
mise install                                    # Go と Terraform のバージョンを揃える
go install goa.design/goa/v3/cmd/goa@v3.30.0    # goa CLI（go.mod の goa と同じ版）。DSL を変えないなら次の行ごと飛ばしてよい（gen/ はコミット済み）
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

`entries` の CRUD と検索、`users`、`tags` は実際の PostgreSQL に対して流す。確かめたいものが SQL 側
（`date` へのキャスト、`RETURNING`、`pgx.ErrNoRows`、UNIQUE / NOT NULL 制約、`user_id` での絞り込み、
`ILIKE` のエスケープ、インデックスに合わせた並び）に寄っているため、DB をモックすると肝心なところが残らない。`http_test.go` は Goa の生成コードごと
`httptest` で立てて、Authorization ヘッダ → 401 / `user_id` の流れを HTTP で確かめる。
JWT の発行・検証（`auth_test.go`）だけは DB を使わない。

接続先は `TEST_DATABASE_URL` で渡す。**テストは `entries` / `users` / `tags` テーブルを空にする**ので、
開発用の DB とは別のデータベースを指すこと。`DATABASE_URL` ではなく専用の変数を
見ているのは、取り違えて開発中のデータを消さないため。

```sh
docker compose exec db psql -U life -d postgres -c 'CREATE DATABASE life_test'
TEST_DATABASE_URL='postgres://life:life@localhost:5432/life_test?sslmode=disable' go test ./...
```

`POSTGRES_PORT` をずらしているなら、URL のポートもそれに合わせる（5433 なら `localhost:5433`）。

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
- `user_id` はリクエストでは受け取らず、作成時にトークンの `sub` から入れる（DB では NOT NULL）
- `entries` はすべて自分の記録だけが対象。一覧は自分の分だけ返し、id を指定する取得・更新・削除は
  他人の id を「存在しない」と同じ 404 にする（403 だと、その id が存在することが分かってしまう。
  404 なら、あるかどうかを判断できない）
- 000003 で `user_id` を NOT NULL にした。それより前に作られた `user_id` が NULL の行
  （誰からも見えなくなる）は、このマイグレーションで消える

DSL では `JWTSecurity("jwt")` を定義し、`entries` サービスと `users.me` に `Security(JWTAuth)` を
付けている。Token 属性は `Required` にしていない。必須にするとヘッダ無しが Goa のデコード段階で
400 になるため、空のまま `JWTAuth` まで通して 401 にしている。

### tags

タグは `entries` の `tags`（名前の配列）で付け外しする。無い名前はそのユーザのタグとして作られ、
ある名前は紐付けだけ増える。PUT は全置換なので、`tags` を省くと全部外れる（`body` と同じ扱い）。
レスポンスの `tags` は名前順で、無ければ `[]`。

`tags` サービスは一覧（`GET /tags`、件数付き・名前順）・改名（`PUT /tags/{id}`、全記録に反映）・
削除（`DELETE /tags/{id}`、紐付けごと消え、記録は残る）の 3 つ。`POST /tags` は無い。
要件に「タグを追加で作る」が無いため（タグは entries の `tags` に名前を書いたときに作られる）。
紐付けの無くなったタグは 0 件のまま残る（消したければ delete）。

- 名前は 1〜50 文字、前後に空白なし、大文字小文字は区別する。同じユーザの中で一意で、既にある名前への改名は 409
- テーブルは `tags`（`user_id` + `name` で一意）と `entry_tags`（中間テーブル、双方向に CASCADE）。
  タグ検索（#45）用に `(tag_id, entry_id)` のインデックスも張ってある
- 記録の作成・更新はトランザクションで、名前の upsert と紐付けを 1 文でやる（`entries.go` の `setTags`）

### 検索

`GET /entries` のクエリパラメータで絞り込む。指定しなければ全件で、複数指定は全て AND。
並びとページネーション（`limit` / `offset`）は一覧と同じ。別のエンドポイントにしなかったのは、
どれも絞り込みであり、並びとページネーションを二重に持ちたくないため。

| パラメータ | 意味 |
| --- | --- |
| `tag=go&tag=aws` | このタグが**全て**付いている記録（繰り返しで AND） |
| `q=goa` | `title` か `body` にこの文字列を含む記録。大文字小文字は区別しない。`% _ \` もそのままの文字として探す |
| `from=2026-09-01` / `to=2026-09-30` | 記録日（`entry_date`）の範囲。両端を含む。片方だけでもよい |

- 期間は `entry_date`（日付）だけで見る。`created_at` は timestamptz で、Fargate（UTC）とローカル（+09:00）で
  日付の境界がずれるため検索には使わない（09/05 の気づき）
- `q` は PostgreSQL の全文検索（tsvector）ではなく `ILIKE` の部分一致。tsvector は日本語を分かち書き
  できない。1 人分の記録量なら `user_id` で絞ったあとの走査で足りるので、インデックスも張っていない
- 値が空のパラメータ（`?q=`）は Goa が未指定として扱う。`from > to` は 400 ではなく空の一覧

### 打鍵確認（curl）

entries の CRUD は `entries` テーブルへの読み書き（`entries.go`）。プロセスを再起動しても
データは残る。一覧は自分の記録を記録日の新しい順（同じ日なら id の降順）で、**既定 10 件**返す。
件数と位置は `limit`（1〜100）と `offset` でずらす。

`/health` はプロセスが生きているかに加えて DB への疎通も見る。繋がらないときは 503 を
返すので、ALB のターゲットグループから外れる（life#48 で繋いだ）。

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
# tags は任意で、名前順に返る
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/entries \
  -d '{"title":"Goa入門","entry_date":"2026-08-31","kind":"til","body":"本文","tags":["goa","go"]}'

# 一覧（既定 10 件）/ 単体取得
curl -H "Authorization: Bearer $TOKEN" localhost:8080/entries
curl -H "Authorization: Bearer $TOKEN" 'localhost:8080/entries?limit=3&offset=10'   # 11 件目から 3 件
curl -H "Authorization: Bearer $TOKEN" localhost:8080/entries/1

# 検索（タグは繰り返しで AND、q は部分一致、from / to は記録日で両端を含む。全て組み合わせ可）
curl -H "Authorization: Bearer $TOKEN" 'localhost:8080/entries?tag=go&tag=goa'
curl -H "Authorization: Bearer $TOKEN" 'localhost:8080/entries?q=goa'
curl -H "Authorization: Bearer $TOKEN" 'localhost:8080/entries?from=2026-09-01&to=2026-09-30'
curl -H "Authorization: Bearer $TOKEN" 'localhost:8080/entries?tag=go&q=JWT&from=2026-09-01&limit=5'

# 更新（created_at と user_id は維持され、updated_at だけ進む。
#  body や tags を省くと NULL / [] に戻る＝PUT なので送った内容で全体を置き換える）
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/entries/1 \
  -d '{"title":"Goa入門(更新)","entry_date":"2026-08-31","kind":"til","body":"追記","tags":["goa"]}'

# タグの一覧（件数付き）/ 改名（全記録に反映）/ 削除（紐付けごと消え、記録は残る）
curl -H "Authorization: Bearer $TOKEN" localhost:8080/tags
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/tags/1 \
  -d '{"name":"golang"}'
curl -i -X DELETE -H "Authorization: Bearer $TOKEN" localhost:8080/tags/1

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

# 異常系: 存在しない id と他人の id はどちらも 404 not_found
curl -i -H "Authorization: Bearer $TOKEN" localhost:8080/entries/999

# 異常系: バリデーション違反は 400
#（kind は til | diary のみ、title / entry_date / kind は必須。
#  title の空文字と YYYY-MM-DD でない entry_date も 400 になる）
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/entries -d '{"title":"x"}'
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:8080/entries \
  -d '{"title":"","entry_date":"banana","kind":"til"}'
```

### ログ（1 リクエスト 1 行の JSON）

出力先は標準出力。端末なら色付きのテキスト、それ以外（`docker compose logs`、Fargate → CloudWatch Logs）は
JSON になる（`cmd/life/main.go` の `log.IsTerminal()`。Goa の雛形のまま）。
ローカルで JSON を見たければ `go run ./cmd/life 2>&1 | cat` のようにパイプに通す。

リクエストは終わったときに 1 行だけ出す（`reqlog.go` の `RequestLog`。雛形の `log.HTTP` は
start / end の 2 行で request id も自前の乱数だったので差し替えた。life#50）。

```json
{"time":"2026-09-17T12:00:00Z","level":"info","request_id":"Root=1-6aaa84c0-0123456789abcdef01234567","msg":"request","method":"GET","path":"/entries","status":200,"duration_ms":4,"bytes":213,"remote_addr":"203.0.113.9","user_id":1}
```

| キー | 中身 |
| --- | --- |
| `request_id` | ALB が付ける `X-Amzn-Trace-Id`（`Root=1-<時刻の hex>-<乱数>`）。ALB を通らないとき（ローカル・テスト）は自前の乱数。同じリクエストの中で出る他の行（`users.login`、`ERROR id=...`）にも同じ値が付く |
| `method` / `path` | クエリ文字列は含めない（検索語が入る） |
| `status` / `duration_ms` / `bytes` | レスポンスのステータス・所要時間（ミリ秒）・書いたバイト数 |
| `remote_addr` | クライアントの IP。`X-Forwarded-For` の末尾（ALB が足した値。先頭側はクライアントが書ける）、無ければ接続元 |
| `user_id` | JWT の `sub`。`JWTAuth` を通ったリクエストにだけ付く（401 や `POST /users/login` には無い）。サービスの行にも付く |

パスワード・トークン本体・リクエストボディは出さない（`-debug` のときだけ `debug.HTTP` がボディを
別の行に出す。`users` はそこでも対象外）。ハンドラが panic したときは `status` 500 と `panic: true` で
行を残し、スタックは net/http が stderr に出す。

CloudWatch Logs Insights（ロググループ `/ecs/life-api`）で `user_id` と `status` で絞る例:

```
fields @timestamp, method, path, status, duration_ms, request_id
| filter msg = "request" and user_id = 1 and status >= 400
| sort @timestamp desc
| limit 50
```

CLI からは `start-query` → `get-query-results`（結果が揃うまで数秒。`status` が `Complete` になるまで叩き直す）:

```sh
QUERY_ID=$(aws logs start-query --log-group-name /ecs/life-api \
  --start-time $(( $(date +%s) - 3600 )) --end-time $(date +%s) \
  --query-string 'fields @timestamp, method, path, status, duration_ms, request_id | filter msg = "request" and user_id = 1 and status >= 400 | sort @timestamp desc | limit 50' \
  --query queryId --output text)
aws logs get-query-results --query-id "$QUERY_ID"
```

ローカルで request id の受け渡しを見るには、ALB の代わりにヘッダを自分で付ける:

```sh
curl -H 'X-Amzn-Trace-Id: Root=1-00000000-000000000000000000000000' localhost:8080/health
docker compose logs --no-log-prefix api | grep '"request"' | tail -1 | jq .   # request_id に上の値が入る
```

## インフラ（Terraform）

AWS 側の構成は `terraform/` にある。W1〜W2 でコンソールと CLI で手作業したものを
そのまま Terraform に書き起こし（life#40）、W4 で ALB を前に置き（life#48）、
デフォルト VPC から独自 VPC に移した（life#49）。VPC はパブリックサブネット 2 つだけで、
ALB / ECS タスク / RDS を全部そこに置く。
外からの入口は ALB の 80 だけで、api の 8080 には ALB の SG からしか届かない（構成図は冒頭の「構成」）。

| ファイル | 中身 |
| --- | --- |
| `alb.tf` | ALB `life-api`（internet-facing、HTTP 80 のみ）、ターゲットグループ（`target_type = "ip"`、ヘルスチェック `/health`）、リスナー |
| `ecr.tf` | リポジトリ `life-api` と、新しい 10 世代だけ残すライフサイクル |
| `ecs.tf` | クラスタ `life`、タスク定義 `life-api` / `life-migrate`、サービス `life-api`（起動したタスクをターゲットグループに登録） |
| `rds.tf` / `ssm.tf` | RDS `life-db`（db.t4g.micro）と、SSM パラメータ `/life-api/database-url`（接続文字列）/ `/life-api/jwt-secret`（JWT の署名鍵。Terraform が生成し、api にだけ渡す） |
| `sg.tf` | SG `life-api-alb`（80 は全開、出口は api の 8080 だけ）/ `life-api-ecs`（8080 は ALB の SG からだけ）/ `life-api-rds`（5432 は api の SG からだけ） |
| `iam.tf` | タスク実行ロール `ecsTaskExecutionRole`（ECR pull / ログ / SSM 読み取り） |
| `logs.tf` | ロググループ `/ecs/life-api`（30 日で消す）。中身の形は上の「ログ」の節 |
| `vpc.tf` | VPC `life`（10.0.0.0/16）、パブリックサブネット `life-public-a` / `life-public-c`、IGW、0.0.0.0/0 を IGW に向けたルートテーブル |
| `data.tf` | アカウント ID と、ネットワークの参照を 1 箇所に寄せた locals（`vpc_id` / `public_subnet_ids`）。SG / ECS / RDS / ALB はここ経由で VPC を見る |

### 使い方

前提: AWS CLI v2 と `life` プロファイル（VPC / ELB / ECS / ECR / RDS / IAM / SSM / Logs を作れる権限）、
Terraform（`mise install` で入る）、Docker、打鍵用に `jq`。

```sh
export AWS_PROFILE=life
cd terraform
terraform init
terraform plan
terraform apply                                 # RDS 込みで 5〜10 分かかる
```

**初めて立てるときは次の順で通す。** apply の直後は ECR が空なので、イメージを push するまで
migrate も api も起動できない（`CannotPullContainerError`）。サービスは desired 0 で作られるので、
apply 自体は ECR が空でも通る。

1. `terraform apply`（上）
2. 「イメージの更新」で build / push（最後の `--force-new-deployment` は desired 0 なので省いてよい）
3. 「スキーマ適用と起動・停止」で migrate → desired 1 → `/health`
4. 「打鍵確認（curl）」の `localhost:8080` を `$(terraform output -raw api_url)` に読み替えて打鍵
5. desired 0 に戻し、`-var db_enabled=false` で RDS を消す

state は手元の `terraform.tfstate`（gitignore 済み）。DB のパスワードが平文で入るので、
リポジトリにもイメージにも入れない（`.dockerignore` で `terraform/` ごと外している）。

`terraform.tfvars` は要らない（渡す変数が無い。既定値のままで通る）。

### スキーマ適用と起動・停止

DB は空で作られるので、最初に `life-migrate up` を Fargate の単発タスクとして流す。
VPC 内から RDS に繋ぐので、09/05 のように自宅 IP を RDS の SG に開ける必要はない。

```sh
terraform output -raw migrate_command | sh      # 単発タスクを起動（終了コード 0 で成功）
aws logs tail /ecs/life-api --log-stream-name-prefix migrate --since 10m   # ログで確認

aws ecs update-service --cluster life --service life-api --desired-count 1   # api を 1 タスク起動
curl "$(terraform output -raw api_url)/health"  # ALB 経由。ターゲットが healthy になるまで 1 分ほど
aws ecs update-service --cluster life --service life-api --desired-count 0   # 確認が済んだら止める
terraform apply -var db_enabled=false           # RDS と接続文字列を消す（ECR / ECS / SG / IAM / ALB は残る）
```

**desired count は Terraform の変数（既定 0）で持ち、起動と停止は CLI でやる。**
`terraform apply -var api_desired_count=1` でも起動できるが、CLI なら state の desired が 0 のままなので、
止めたあとの `terraform plan` に差分が出ない（09/14）。CLI で 1 にしても次の `terraform apply` で 0 に戻る。
「使い終わったら止める」を既定にするための挙動なので、そのまま使う。

URL は `terraform output -raw api_url`（`http://<ALB の DNS 名>`）。タスクを起動し直しても変わらないので、
「打鍵確認（curl）」の `localhost:8080` をこれに読み替えればそのまま通る。
タスクのパブリック IP を直接叩く経路は無い（8080 には ALB の SG からしか届かない）。

### RDS を使わない間は消す判断

- **止めるのではなく消す。** これもキャッチアップのための環境で、停止では費用がかかるので、使わない間は常に削除する。
  停止中もストレージ（20GB gp3 で $2.76/月）とバックアップの課金は続き、止めても 7 日で勝手に起動する
  （気づかなければ $0.025/時がそのまま乗る）
- 消すときにスナップショットは残さない（残すと課金が続く）ので、データは毎回無くなる。
  次に使うときは `terraform apply`（5〜10 分）で空の DB を作り直し、スキーマ適用からやり直す

### 接続文字列と JWT の鍵を SSM に置く判断

- **無料で足りるので SSM にした。** Standard の SecureString は無料で、Secrets Manager は 1 件 $0.40/月。
  Secrets Manager の売りは自動ローテーションだが、ここでは使っていない
- タスク定義には `environment` ではなく `secrets` で渡す。タスク定義の JSON に載るのはパラメータ名だけで、
  値は起動時に実行ロールが SSM から引く（`environment` だと平文がタスク定義に残る）

### ALB を前に置いたときの判断（life#48）

- **ALB を独自 VPC より先にした。** 「人に見せられる」に直結するのは固定の URL で、それをくれるのが ALB。
  遅れたときに削るのは VPC の側。VPC / サブネットの参照は `data.tf` の locals（`vpc_id` / `public_subnet_ids`）に
  寄せてあり、VPC を移すときはそこを差し替えるだけで、SG / ECS / RDS / ALB のファイルは触らない
- **入口は SG の参照で連鎖させる。** CIDR で開けるのは ALB の 80 だけ。api の 8080 は ALB の SG から、
  RDS の 5432 は api の SG から。ALB のノードやタスクの IP が変わっても効く。
  タスクのパブリック IP は出口として残す（理由は下の「独自 VPC に移したときの判断」）が、入口としては使えない
- **HTTP のみ。** HTTPS と独自ドメインは goals に無い。証明書と Route 53 が要るので、必要になったときに足す
- **ヘルスチェックは `/health`。** DB への疎通も見るので、DB に繋がらないタスクは 503 で外れる。
  起動時に DB へ繋ぎに行く分、サービスに `health_check_grace_period_seconds = 60` の猶予を付けた。
  ターゲットの登録解除は 30 秒（既定の 300 秒だと desired 0 に戻すときに待たされる）
- **トグルは付けない。** 立てた瞬間から課金されるが、消すと DNS 名が変わって渡した URL が死ぬ。
  W4 の間は立てたままにし、残すかは週末に決める
- **サービスに `load_balancer` を後から付けても作り直しにはならない。** issue では作り直しを見込んで desired 0 の
  タイミングで足したが、provider 6.x の plan は `update in-place`（`health_check_grace_period_seconds` も同時に in-place）だった

### 独自 VPC に移したときの判断（life#49）

```
VPC life  10.0.0.0/16（DNS 解決・ホスト名 有効）
├── life-public-a  10.0.0.0/24  ap-northeast-1a  ┐ ALB・ECS タスク・RDS のサブネットグループはこの 2 つ
└── life-public-c  10.0.1.0/24  ap-northeast-1c  ┘ ルートテーブル life-public: 0.0.0.0/0 → IGW
```

- **パブリックサブネットだけ。プライベートサブネットも NAT ゲートウェイも VPC エンドポイントも持たない。**
  このサービスは自分一人が趣味で使うもので、本番運用は考えていない。個人で使うので、費用は極力抑えたい。
  タスクをプライベートに置くと、ECR の pull / SSM / CloudWatch Logs への出口が別に要る。
  NAT ゲートウェイは $0.062/時（月 45 ドル）+ 転送量、VPC エンドポイントは ecr.api / ecr.dkr / ssm / logs の
  4 つ × AZ 数で、1 AZ に絞っても $0.056/時。どちらも ALB（パブリック IPv4 込みで ~$0.034/時）より高い。
  入口は SG で ALB だけに絞れていて、タスクにパブリック IP が付いていても直接は叩けない（09/15 に 8080 直叩きの
  タイムアウトを確認済み）。
  これが本番なら、タスクと RDS をプライベートサブネットに置き、NAT かエンドポイントを持つ
- **W1 はデフォルト VPC で始めた。** とりあえず、ありものを使いたかった（VPC / パブリックサブネット / IGW / ルートが
  最初から揃っていて、ネットワークを何も書かずにタスクを置ける）
- **構成はデフォルト VPC と同じで、自分で書いただけ。** サブネットは 2 つ（ALB が 2 AZ 以上を要求する。デフォルト VPC は
  1a / 1c / 1d の 3 つだった）。CIDR は 10.0.0.0/16（デフォルトの 172.31.0.0/16 と重ならない）。
  DNS 解決とホスト名はデフォルト VPC と同じく有効にした。解決は ECR / SSM / RDS の名前を VPC 内から引くのに要り、
  ホスト名は今は使わないが、既定の false にして挙動が変わる箇所を増やさない
- **移行で触ったのは `data.tf` の locals の右辺だけ。** SG / ECS / RDS / ALB のファイルは変えていない（life#48 で
  参照を寄せておいた効果）。ただし `vpc_id` を持つ SG とターゲットグループは作り直しになる。
  **DB のサブネットグループと ALB は provider が「in-place 更新」で計画するが、AWS 側が VPC をまたぐ変更を受け付けない**ので
  `-replace` で作り直す（手順は `terraform/RUNBOOK.md`）。ALB を作り直すので DNS 名（`api_url`）は変わる。
  RDS は消してあったので巻き込まれるものが無かった
- **デフォルト VPC は消していない。** もう参照していないが、残しても課金は無く、消すと戻す手間
  （`aws ec2 create-default-vpc`）だけ増える

### イメージの更新

`--platform linux/amd64` と `--provenance=false` の 2 つは省かない（08/29・09/05 の TIL）。
前者が無いと arm64 Mac のイメージになって Fargate で動かず、後者が無いとイメージが
OCI index になってタグなしの実体が並ぶ。
`${repo}` の波括弧も省かない。zsh は `$repo:latest` の `:l` を小文字化の修飾子と読み、
タグが `life-apiatest`（`:latest` が消えて `atest` だけ残る）になって push が「repository が無い」で落ちる（09/14 の TIL）。

```sh
repo=$(terraform -chdir=terraform output -raw ecr_repository_url)
aws ecr get-login-password | docker login --username AWS --password-stdin "${repo%/*}"
docker build --platform linux/amd64 --provenance=false -t "${repo}:latest" .
docker push "${repo}:latest"
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
| ALB | $0.0243/時 + パブリック IPv4 2 個（$0.005/時 × 2 AZ）= ~$0.034/時（~$25/月）+ LCU | 消すしかない（DNS 名が変わる）。W4 の間は残す |
| VPC / サブネット / IGW / ルートテーブル | 0 | - （NAT ゲートウェイと VPC エンドポイントは持たない） |
| RDS db.t4g.micro + 20GB gp3 | $0.025/時 + ストレージ $2.76/月（~$21/月） | `-var db_enabled=false` で消す |
| Fargate 0.25 vCPU / 0.5 GB | ~$0.015/時 + タスクのパブリック IPv4 $0.005/時 | desired 0（既定） |
| ECR / ログ / SSM Standard | ほぼ 0 | - |

単価は ap-northeast-1 のオンデマンド（2026-09-19 に AWS Pricing API で確認）。

## 関連

- 目標・進め方・週次の計画は [life](https://github.com/e601201/life) リポジトリの `state/` に置いている
- 日々の進捗は life の `journal/YYYY/MM/DD.md` に日報として書く。このリポジトリにはコードのみ置く
