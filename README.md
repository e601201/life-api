# life-api

いま Markdown で書いている TIL / 日誌を、API として読み書きできるようにするサービス。
リソースは `entries`（TIL）/ `tags` / `users` の3つで、JWT 認証とタグ・全文・期間での検索を持つ。
Goa の design-first で DSL から OpenAPI を生成し、Go + ECS Fargate + RDS PostgreSQL 上で動かす。

## 開発

```
mise install                                    # Go のバージョンを揃える
goa gen github.com/e601201/life-api/design      # design.go から gen/ を生成
go run ./cmd/life --host localhost              # http://localhost:8080 で起動
curl localhost:8080/health                      # => "server OK!"
```

`--host container` を渡すと `0.0.0.0:8080` で listen する。コンテナや ECS ではこちらを使う
（`localhost` だと 127.0.0.1 に bind されるため、コンテナの外から繋がらない）。
bind するアドレスは `design/design.go` の `Server` / `Host` で定義しているので、
変えるときは `cmd/` ではなく DSL を直して `goa gen` し直す。

### docker compose（api + PostgreSQL）

api と PostgreSQL をまとめて立ち上げる。`depends_on` の `service_healthy` で、
db が接続を受け付けられるようになってから api が起動する。

```sh
cp .env.example .env              # 任意。DB のユーザ名やポートを変えたいときだけ
docker compose up -d --build      # 起動
curl localhost:8080/health        # => "server OK!"
docker compose logs -f api        # ログ
docker compose down               # 停止（-v を付けると DB のデータも消える）
```

DB はホストの 5432 に出している。ローカルで別の Postgres が動いていると bind に失敗するので、
そのときは `POSTGRES_PORT` でずらす（コンテナ間は `db:5432` のままなので api には影響しない）。

```sh
psql -h 127.0.0.1 -p 5432 -U life -d life   # パスワードは life
```

api には接続先を `DATABASE_URL`（`postgres://life:life@db:5432/life?sslmode=disable`）で
渡している。サービス実装はまだインメモリなので、この値は今のところ使っていない。
データは名前付きボリューム `pgdata` に残る（Postgres 18 で `PGDATA` の位置が変わったため、
マウント先は `/var/lib/postgresql/data` ではなく `/var/lib/postgresql`）。

### コード生成の注意

- **`goa gen` は何度実行してもよい。** `gen/` を作り直すだけで、手を入れたコードは壊さない
- **`goa example` は初回のみ。** `cmd/` とサービス実装（`health.go`）を生成するが、
  再実行すると**自分で書いた実装を雛形で上書きする**。
  API 名やメソッド名を変えたときは `goa gen` だけ流し、`cmd/` と実装は手で追随させる
- `gen/` はコミットする。Docker のビルドステージで goa CLI を入れずに済むため

### 打鍵確認（curl）

POST / PUT は `-H 'Content-Type: application/json'` が必須。
`-d` だけだと curl は form-urlencoded で送るため、Goa が 415 を返す。

```sh
# health
curl localhost:8080/health

# 作成（id・created_at・updated_at はサーバー側で付与。Location ヘッダに新リソースのパスが入る）
# user_id もリクエストでは受け取らない（W3 で JWT から入れる）
curl -H 'Content-Type: application/json' localhost:8080/entries \
  -d '{"title":"Goa入門","entry_date":"2026-08-31","kind":"til","body":"本文"}'

# 一覧 / 単体取得
curl localhost:8080/entries
curl localhost:8080/entries/1

# 更新（created_at は維持され、updated_at だけ進む）
curl -X PUT -H 'Content-Type: application/json' localhost:8080/entries/1 \
  -d '{"title":"Goa入門(更新)","entry_date":"2026-08-31","kind":"til","body":"追記"}'

# 削除（204 No Content）
curl -i -X DELETE localhost:8080/entries/1

# 異常系: 存在しない id は 404 not_found
curl -i localhost:8080/entries/999

# 異常系: バリデーション違反は 400
#（kind は til | diary のみ、title / entry_date / kind は必須。
#  title の空文字と YYYY-MM-DD でない entry_date も 400 になる）
curl -H 'Content-Type: application/json' localhost:8080/entries -d '{"title":"x"}'
curl -H 'Content-Type: application/json' localhost:8080/entries \
  -d '{"title":"","entry_date":"banana","kind":"til"}'
```

## 関連

- 目標・進め方・週次の計画は [life](https://github.com/e601201/life) リポジトリの `state/` に置いている
- 日々の進捗は life の `journal/YYYY/MM/DD.md` に日報として書く。このリポジトリにはコードのみ置く
