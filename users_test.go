package life

// users の登録・ログイン・自分の情報。entries と同じく実際の PostgreSQL に対して流す
// （UNIQUE 制約違反の扱いと lower(email) の検索が確かめたいもののため）。

import (
	"context"
	"strings"
	"testing"

	users "github.com/e601201/life-api/gen/users"
)

// newUsersService はテスト用の users サービスを返す。テーブルは空にする。
func newUsersService(t *testing.T) (users.Service, context.Context) {
	t.Helper()
	ctx := resetTables(t)
	return NewUsers(testPool, testAuth), ctx
}

// mustRegister は前提となるユーザを作る。
func mustRegister(t *testing.T, ctx context.Context, svc users.Service, email, password string) *users.User {
	t.Helper()
	u, err := svc.Register(ctx, &users.Credentials{Email: email, Password: password})
	if err != nil {
		t.Fatalf("register %q: %v", email, err)
	}
	return u
}

func TestRegister(t *testing.T) {
	svc, ctx := newUsersService(t)

	got, err := svc.Register(ctx, &users.Credentials{Email: "alice@example.com", Password: "correct horse"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if got.ID != 1 {
		t.Errorf("id = %d, want 1", got.ID)
	}
	if got.Email != "alice@example.com" {
		t.Errorf("email = %q, want %q", got.Email, "alice@example.com")
	}
	if got.CreatedAt == "" {
		t.Error("created_at が空")
	}

	// 平文は持たない。bcrypt のハッシュ（$2a$ 始まり）だけが残っていること。
	var hash string
	if err := testPool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, got.ID).Scan(&hash); err != nil {
		t.Fatalf("select: %v", err)
	}
	if hash == "correct horse" || !strings.HasPrefix(hash, "$2a$") {
		t.Errorf("password_hash = %q, bcrypt のハッシュではない", hash)
	}
}

func TestRegisterConflictIgnoresCase(t *testing.T) {
	svc, ctx := newUsersService(t)

	mustRegister(t, ctx, svc, "Alice@Example.com", "correct horse")

	// 大文字小文字だけ違う email は同じ人。users_email_lower_idx が弾く。
	_, err := svc.Register(ctx, &users.Credentials{Email: "alice@example.com", Password: "another pass"})
	requireErrorName(t, err, "conflict")
}

func TestRegisterPasswordTooLong(t *testing.T) {
	svc, ctx := newUsersService(t)

	// 25 文字（DSL の MaxLength(72) は通る）だが 75 バイトで、bcrypt の 72 バイトを超える。
	password := strings.Repeat("あ", 25)
	_, err := svc.Register(ctx, &users.Credentials{Email: "bob@example.com", Password: password})
	requireErrorName(t, err, "password_too_long")

	// 24 文字（72 バイト）はちょうど上限で通る。
	if _, err := svc.Register(ctx, &users.Credentials{Email: "bob@example.com", Password: password[:len(password)-len("あ")]}); err != nil {
		t.Errorf("72 バイトちょうどが拒否された: %v", err)
	}
}

func TestLogin(t *testing.T) {
	svc, ctx := newUsersService(t)

	registered := mustRegister(t, ctx, svc, "alice@example.com", "correct horse")

	// email は登録時と大文字小文字が違っても通る。
	got, err := svc.Login(ctx, &users.LoginPayload{Email: "ALICE@example.com", Password: "correct horse"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", got.TokenType)
	}
	if got.ExpiresIn != int(tokenTTL.Seconds()) {
		t.Errorf("expires_in = %d, want %d", got.ExpiresIn, int(tokenTTL.Seconds()))
	}

	// 発行されたトークンは同じ鍵で検証でき、sub が登録したユーザを指すこと。
	uid, err := testAuth.Verify(got.AccessToken)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if uid != registered.ID {
		t.Errorf("sub = %d, want %d", uid, registered.ID)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	svc, ctx := newUsersService(t)

	mustRegister(t, ctx, svc, "alice@example.com", "correct horse")

	// パスワード違いと未登録の email は、同じ名前・同じ文言で返す。
	// 文言を分けると登録済みの email を外から列挙できてしまう。
	var messages []string
	for _, tt := range []struct {
		name  string
		email string
	}{
		{name: "パスワードが違う", email: "alice@example.com"},
		{name: "未登録の email", email: "nobody@example.com"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Login(ctx, &users.LoginPayload{Email: tt.email, Password: "wrong password"})
			serr := requireErrorName(t, err, "unauthorized")
			messages = append(messages, serr.Message)
		})
	}
	if len(messages) == 2 && messages[0] != messages[1] {
		t.Errorf("文言が違う: %q vs %q", messages[0], messages[1])
	}
}

func TestMe(t *testing.T) {
	svc, ctx := newUsersService(t)

	registered := mustRegister(t, ctx, svc, "alice@example.com", "correct horse")

	// HTTP では JWTAuth が ctx にユーザ ID を入れる。ここでは直接入れる。
	got, err := svc.Me(ContextWithUserID(ctx, registered.ID), &users.MePayload{})
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if got.ID != registered.ID || got.Email != "alice@example.com" {
		t.Errorf("me = %d/%q, want %d/%q", got.ID, got.Email, registered.ID, "alice@example.com")
	}
}

func TestMeWithoutUser(t *testing.T) {
	svc, ctx := newUsersService(t)

	_, err := svc.Me(ctx, &users.MePayload{})
	requireErrorName(t, err, "unauthorized")
}

func TestMeUserDeleted(t *testing.T) {
	svc, ctx := newUsersService(t)

	registered := mustRegister(t, ctx, svc, "alice@example.com", "correct horse")
	if _, err := testPool.Exec(ctx, `DELETE FROM users WHERE id = $1`, registered.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// トークンは有効でもユーザが居なければ認証できない。
	_, err := svc.Me(ContextWithUserID(ctx, registered.ID), &users.MePayload{})
	requireErrorName(t, err, "unauthorized")
}
