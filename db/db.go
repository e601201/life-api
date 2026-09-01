// Package db は PostgreSQL への接続と、スキーマのマイグレーションを扱う。
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect は url に対する接続プールを作り、1本張って疎通を確かめてから返す。
//
// pgxpool.New は接続を張らずに返る（最初のクエリまで遅延する）ため、Ping しないと
// URL やパスワードが間違っていても起動は成功してしまい、最初のリクエストで初めて
// 失敗する。起動時点で落としたいのでここで確かめる。
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
