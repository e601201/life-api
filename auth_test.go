package life

// JWT の発行と検証。DB は要らないので TEST_DATABASE_URL が無くても走る。

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// newTestAuth は時刻を固定した Auth を返す。期限切れを決定的に作るため。
func newTestAuth(t *testing.T, now time.Time) *Auth {
	t.Helper()
	a, err := NewAuth(testSecret)
	if err != nil {
		t.Fatalf("new auth: %v", err)
	}
	a.now = func() time.Time { return now }
	return a
}

func TestNewAuthRejectsShortSecret(t *testing.T) {
	// HS256 の鍵は 256 bit 以上（RFC 7518 §3.2）。短い鍵は起動時点で落とす。
	if _, err := NewAuth("short"); err == nil {
		t.Fatal("短い鍵を受け付けている")
	}
	if _, err := NewAuth(""); err == nil {
		t.Fatal("空の鍵を受け付けている")
	}
}

func TestIssueAndVerify(t *testing.T) {
	a := newTestAuth(t, time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))

	token, err := a.Issue(42)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := a.Verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != 42 {
		t.Errorf("user id = %d, want 42", got)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	issuer := newTestAuth(t, now)
	other, err := NewAuth("another-secret-of-sufficient-length-1234")
	if err != nil {
		t.Fatalf("new auth: %v", err)
	}
	other.now = issuer.now

	token, err := issuer.Issue(1)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := other.Verify(token); err == nil {
		t.Fatal("別の鍵で署名したトークンを通している")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	issuedAt := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	a := newTestAuth(t, issuedAt)

	token, err := a.Issue(1)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// 期限の 1 秒前は通り、1 秒後は落ちる。
	a.now = func() time.Time { return issuedAt.Add(tokenTTL - time.Second) }
	if _, err := a.Verify(token); err != nil {
		t.Errorf("期限内なのに拒否した: %v", err)
	}
	a.now = func() time.Time { return issuedAt.Add(tokenTTL + time.Second) }
	if _, err := a.Verify(token); err == nil {
		t.Error("期限切れのトークンを通している")
	}
}

func TestVerifyRejectsUnsignedToken(t *testing.T) {
	a := newTestAuth(t, time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))

	// alg=none。署名を検証しない実装だと、誰でも好きな sub を名乗れる。
	claims := jwt.RegisteredClaims{
		Issuer:    tokenIssuer,
		Subject:   "1",
		ExpiresAt: jwt.NewNumericDate(a.now().Add(time.Hour)),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := a.Verify(token); err == nil {
		t.Fatal("alg=none のトークンを通している")
	}
}

func TestVerifyRejectsOtherIssuer(t *testing.T) {
	a := newTestAuth(t, time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))

	// 同じ鍵・同じ alg だが iss が違う（鍵を使い回した別サービスのトークン）。
	claims := jwt.RegisteredClaims{
		Issuer:    "someone-else",
		Subject:   "1",
		ExpiresAt: jwt.NewNumericDate(a.now().Add(time.Hour)),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := a.Verify(token); err == nil {
		t.Fatal("iss の違うトークンを通している")
	}
}

func TestVerifyRequiresExpiration(t *testing.T) {
	a := newTestAuth(t, time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))

	// exp の無いトークンは永久に有効になるので、正しく署名されていても拒否する。
	claims := jwt.RegisteredClaims{Issuer: tokenIssuer, Subject: "1"}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := a.Verify(token); err == nil {
		t.Fatal("exp の無いトークンを通している")
	}
}

func TestJWTAuth(t *testing.T) {
	a := newTestAuth(t, time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))
	valid, err := a.Issue(7)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// 失敗はどれも design の unauthorized（HTTP では 401）で、文言から理由が読めないこと。
	for _, tt := range []struct {
		name  string
		token string
	}{
		{name: "ヘッダ無し（空文字）", token: ""},
		{name: "JWT の形をしていない", token: "garbage"},
		{name: "署名が壊れている", token: valid[:len(valid)-3] + "xxx"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := a.JWTAuth(context.Background(), tt.token, nil)
			serr := requireErrorName(t, err, "unauthorized")
			if serr.Message != "missing token" && serr.Message != "invalid token" {
				t.Errorf("message = %q, 理由を外に出している", serr.Message)
			}
		})
	}

	t.Run("有効なトークン", func(t *testing.T) {
		ctx, err := a.JWTAuth(context.Background(), valid, nil)
		if err != nil {
			t.Fatalf("auth: %v", err)
		}
		id, ok := UserIDFromContext(ctx)
		if !ok || id != 7 {
			t.Errorf("user id in ctx = %d (ok=%v), want 7", id, ok)
		}
	})
}

func TestUserIDFromContextWithoutUser(t *testing.T) {
	if id, ok := UserIDFromContext(context.Background()); ok {
		t.Errorf("何も入れていない ctx から %d が取れている", id)
	}
}
