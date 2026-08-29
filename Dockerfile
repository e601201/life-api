# syntax=docker/dockerfile:1

# ---- ビルドステージ ----------------------------------------------------------
# gen/ をコミットしているので、このステージに goa CLI は要らない（README 参照）。
FROM golang:1.27-bookworm AS build

WORKDIR /src

# go.mod / go.sum だけ先に COPY する。ソースを直したときに
# go mod download のレイヤーキャッシュが落ちないようにするため。
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO_ENABLED=0: net/os/user を純 Go 実装にして libc への動的リンクを外す。
#   これで distroless/static（libc なし）でも動くバイナリになる。
# GOARCH=amd64: ECS Fargate の X86_64 に合わせる。
# -trimpath: バイナリからビルドマシンの絶対パスを消す。
# -ldflags="-s -w": シンボルと DWARF を落としてサイズを削る。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/life ./cmd/life

# ---- 実行ステージ ------------------------------------------------------------
# static-debian12: シェルもパッケージマネージャも入っていない。CA 証明書と
# /etc/passwd、tzdata だけ持つ。攻撃面が小さく、イメージも数 MB で済む。
FROM gcr.io/distroless/static-debian12:nonroot

# :nonroot タグは USER が既に nonroot だが、ベースを差し替えたときに
# root に戻らないよう明示しておく。
USER nonroot:nonroot

COPY --from=build /out/life /life

# ドキュメント用（実際の公開は docker run -p / ECS のポートマッピング側で決まる）
EXPOSE 8080

# ENTRYPOINT にバイナリ、CMD に既定の引数を置く。こうしておくと
#   docker run life-api                      -> --host container で起動
#   docker run life-api --host container --debug
# のように、CMD だけ差し替えて引数を足せる。ECS のタスク定義でも
# entryPoint はそのまま、command だけ上書きすればよい。
ENTRYPOINT ["/life"]
CMD ["--host", "container"]
