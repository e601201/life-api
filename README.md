# life-api

いま Markdown で書いている TIL / 日誌を、API として読み書きできるようにするサービス。
リソースは `entries`（TIL）/ `tags` / `users` の3つで、JWT 認証とタグ・全文・期間での検索を持つ。
Goa の design-first で DSL から OpenAPI を生成し、Go + ECS Fargate + RDS PostgreSQL 上で動かす。

## 関連

- 目標・進め方・週次の計画は [life](https://github.com/e601201/life) リポジトリの `state/` に置いている
- 日々の進捗は life の `journal/YYYY/MM/DD.md` に日報として書く。このリポジトリにはコードのみ置く
