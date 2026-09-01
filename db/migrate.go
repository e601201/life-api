package db

import (
	"embed"
	"errors"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	// database/pgx/v5 は init で pgx5 スキーマのドライバを登録する。
	// 直接は呼ばないので blank import。
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// migrations は SQL をバイナリに埋め込む。実行時に .sql を配らずに済むので、
// distroless のイメージにも life-migrate のバイナリ1つを置けばよくなる。
//
//go:embed migrations/*.sql
var migrations embed.FS

// newMigrate は埋め込んだ SQL と接続先から *migrate.Migrate を組み立てる。
func newMigrate(dbURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL(dbURL))
	if err != nil {
		return nil, fmt.Errorf("init migrate: %w", err)
	}
	return m, nil
}

// migrateURL は接続 URL のスキームを pgx5 に差し替える。
//
// golang-migrate は URL のスキームでドライバを選ぶ。postgres:// だと lib/pq 実装が
// 選ばれてしまうので、pgx/v5 実装が登録している pgx5:// に直して渡す。
// パース出来ない場合はそのまま返し、判断は golang-migrate 側のエラーに委ねる。
func migrateURL(dbURL string) string {
	u, err := url.Parse(dbURL)
	if err != nil {
		return dbURL
	}
	switch u.Scheme {
	case "postgres", "postgresql":
		u.Scheme = "pgx5"
	}
	return u.String()
}

// Up は未適用のマイグレーションを全て適用する。適用済みで差分が無ければ何もしない。
func Up(dbURL string) error {
	m, err := newMigrate(dbURL)
	if err != nil {
		return err
	}
	defer m.Close()

	// ErrNoChange は「既に最新」で、失敗ではない。up は何度流してもよいコマンドに
	// しておきたいので、ここで握り潰す。
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Down は n 個ぶんマイグレーションを巻き戻す。
//
// 一括で全部落とす API（m.Down）は用意していない。事故ったときの被害が大きく、
// 巻き戻したい場面はたいてい「直前の1つ」だけのため。
func Down(dbURL string, n int) error {
	if n < 1 {
		return fmt.Errorf("invalid step count: %d", n)
	}
	m, err := newMigrate(dbURL)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Steps(-n); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}

// Version は適用済みのバージョンと dirty フラグを返す。
//
// dirty は「適用の途中で落ちた」印で、立っていると以降の up / down が全て弾かれる。
// その場合は DB の状態を手で直してから force で版を宣言し直すことになる。
// 1件も適用されていないときは version 0 / applied false を返す。
func Version(dbURL string) (version uint, dirty bool, applied bool, err error) {
	m, e := newMigrate(dbURL)
	if e != nil {
		return 0, false, false, e
	}
	defer m.Close()

	v, d, e := m.Version()
	if errors.Is(e, migrate.ErrNilVersion) {
		return 0, false, false, nil
	}
	if e != nil {
		return 0, false, false, fmt.Errorf("migrate version: %w", e)
	}
	return v, d, true, nil
}
