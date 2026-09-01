package life

import (
	"context"
	"errors"
	"time"

	health "github.com/e601201/life-api/gen/health"
	"github.com/jackc/pgx/v5/pgxpool"
	"goa.design/clue/log"
)

// healthPingTimeout は DB への疎通確認を待つ上限。ALB / ECS のヘルスチェックにも
// それ自身のタイムアウトがあるので、待たされる前にこちらで答えを出す。
const healthPingTimeout = 2 * time.Second

// health service の実装。プロセスが生きているかに加えて、DB に繋がるかも見る。
type healthsrvc struct {
	db *pgxpool.Pool
}

// NewHealth returns the health service implementation.
func NewHealth(pool *pgxpool.Pool) health.Service {
	return &healthsrvc{db: pool}
}

// Return OK if the server and its database are alive.
func (s *healthsrvc) Check(ctx context.Context) (string, error) {
	log.Printf(ctx, "health.check")

	// DB に繋がらないまま OK を返すと、実際には何も処理できないタスクが健全と
	// 見なされて残り続ける。ALB のターゲットグループから外れてほしいので 503 にする。
	pingCtx, cancel := context.WithTimeout(ctx, healthPingTimeout)
	defer cancel()
	if err := s.db.Ping(pingCtx); err != nil {
		// 接続先やユーザ名を外に出さないよう、返すのは汎用の文言だけにして
		// 中身はログに残す（entries と同じ扱い）。
		log.Printf(ctx, "health.check: database unreachable: %v", err)
		return "", health.MakeServiceUnavailable(errors.New("database unavailable"))
	}
	return "server OK!", nil
}
