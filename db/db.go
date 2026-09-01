// Package db は PostgreSQL への接続と、スキーマのマイグレーションを扱う。
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// connectTimeout は接続 1 本を張るのに待つ上限。
//
// URL に connect_timeout が無いと pgx は待ち続ける。相手が「到達はするが応答を
// 返さない」状態（セキュリティグループでパケットが落ちている RDS が典型）だと、
// TCP のハンドシェイクが OS のタイムアウトまで数分ブロックする。その間 api は
// 起動もせず終了もしないので、ECS から見るとデプロイがただ止まって見える。
// 短く切って落とし、失敗として扱えるようにする。
const connectTimeout = 5 * time.Second

// Connect は url に対する接続プールを作り、1本張って疎通を確かめてから返す。
//
// pgxpool.New は接続を張らずに返る（最初のクエリまで遅延する）ため、Ping しないと
// URL やパスワードが間違っていても起動は成功してしまい、最初のリクエストで初めて
// 失敗する。起動時点で落としたいのでここで確かめる。
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// URL 側で connect_timeout が指定されていればそちらを尊重する。
	// ここで入れた値は起動時の Ping だけでなく、以降プールが接続を張り直す
	// ときにも効く。
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = connectTimeout
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}

	// ConnectTimeout は接続 1 本ぶんの上限なので、名前解決やリトライを含めた
	// 全体の上限としてここでも期限を切る。
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
