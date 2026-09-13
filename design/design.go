package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = API("life", func() {
	Server("life", func() {
		Host("localhost", func() { URI("http://localhost:8080") })
		Host("container", func() { URI("http://0.0.0.0:8080") })
	})
})

// JWTAuth は Authorization: Bearer <JWT> で認証するスキーム。
// 発行は users.login、検証は各サービスの JWTAuth（auth.go）でやる。
// 署名鍵は環境変数 JWT_SECRET で渡し、リポジトリには置かない。
//
// スコープは定義しない。ユーザは自分のデータしか触れないので、
// 「誰か」が分かれば十分で、権限の粒度はまだ要らない。
var JWTAuth = JWTSecurity("jwt", func() {
	Description("Authorization: Bearer <JWT>. users.login で発行したトークンを渡す")
})

// jwtToken は JWT で守るメソッドの Payload に置くトークン属性。HTTP では
// Authorization ヘッダに暗黙でマップされる（Bearer の接頭辞は生成コードが剥がす）。
//
// Required にしない理由: 必須にするとヘッダ無しのリクエストが Goa のデコード段階で
// 400（missing_field）になる。認証の失敗は 401 で返したいので、空のまま
// JWTAuth まで通して、そこで unauthorized にする。
func jwtToken() {
	Token("token", String, "JWT (Authorization: Bearer <token>)")
}

var _ = Service("health", func() {
	Description("health check this server")

	Method("check", func() {
		Description("Return OK if the server and its database are alive.")
		Result(String, "OK")
		// DB に繋がらないときは 503。ALB / ECS のヘルスチェックが叩く口なので、
		// 実際には何も処理できないタスクを健全と判定させないため。
		Error("service_unavailable")

		HTTP(func() {
			GET("/health")
			Response(StatusOK)
			Response("service_unavailable", StatusServiceUnavailable)
		})
	})
})

// Credentials は登録時に受け取る email / password。パスワードの制約はここに置く。
var Credentials = Type("Credentials", func() {
	Description("Email and password for registration")

	Attribute("email", String, "メールアドレス（大文字小文字は区別しない）", func() {
		Format(FormatEmail)
		// RFC 5321 のアドレス長の上限
		MaxLength(254)
	})
	// 下限は NIST SP 800-63B の推奨（8 文字）。上限はハッシュに使う bcrypt が
	// 72 バイトまでしか見ないため。MaxLength は文字数を数えるので、マルチバイトだと
	// 72 文字以内でも 72 バイトを超えうる。その場合は実装側で password_too_long にする。
	Attribute("password", String, "パスワード（8 文字以上、72 バイト以内）", func() {
		MinLength(8)
		MaxLength(72)
	})

	Required("email", "password")
})

// User は API が返すユーザ。password_hash は絶対に出さない。
var User = Type("User", func() {
	Description("A registered user")

	Attribute("id", Int64)
	Attribute("email", String, func() {
		Format(FormatEmail)
	})
	Attribute("created_at", String, func() {
		Format(FormatDateTime)
	})

	Required("id", "email", "created_at")
})

// TokenResponse は login の結果。フィールド名は OAuth 2.0 のトークンレスポンス
// （RFC 6749 §5.1）に合わせてあるので、汎用のクライアントがそのまま読める。
// （AccessToken は DSL の関数名と衝突するので、この名前にしている）
var TokenResponse = Type("TokenResponse", func() {
	Description("Issued JWT")

	Attribute("access_token", String, "JWT")
	Attribute("token_type", String, "常に Bearer", func() {
		Enum("Bearer")
	})
	Attribute("expires_in", Int, "有効期限までの秒数")

	Required("access_token", "token_type", "expires_in")
})

// Service users: 登録 / ログイン（JWT 発行）/ 自分の情報。
//
// register と login は認証なしで叩ける（トークンを持っていない状態で使うため）。
// me だけ JWT を要求し、トークンの検証が通っていることの確認にも使う。
var _ = Service("users", func() {
	Description("User registration and authentication")

	Method("register", func() {
		Description("Register a new user")
		Payload(Credentials)
		Result(User)
		// 同じ email が既にあれば 409。存在確認と INSERT を分けると競合するので、
		// UNIQUE 制約違反をそのまま 409 にする。
		Error("conflict")
		// bcrypt の上限（72 バイト）を超えるパスワード。DSL の MaxLength は
		// 文字数なので、ここだけは実装側で見る（Credentials のコメント参照）。
		Error("password_too_long")

		HTTP(func() {
			POST("/users")
			Response(StatusCreated)
			Response("conflict", StatusConflict)
			Response("password_too_long", StatusBadRequest)
		})
	})

	Method("login", func() {
		Description("Issue a JWT for the given credentials")
		// 登録時の制約（8 文字以上など）はここでは掛けない。掛けると、後で制約を
		// 厳しくしたときに既存ユーザがログインできなくなる。合っているかどうかは
		// 実装が bcrypt で見るので、ここは「文字列が来ている」ことだけを確かめる。
		Payload(func() {
			Attribute("email", String, "メールアドレス（大文字小文字は区別しない）")
			Attribute("password", String)
			Required("email", "password")
		})
		Result(TokenResponse)
		// email が無いのかパスワードが違うのかは区別せず、どちらも同じ 401 にする。
		// 区別すると登録済みの email を外から列挙できてしまう。
		Error("unauthorized")

		HTTP(func() {
			POST("/users/login")
			Response(StatusOK)
			Response("unauthorized", StatusUnauthorized)
		})
	})

	Method("me", func() {
		Description("Return the authenticated user")
		Security(JWTAuth)
		Payload(jwtToken)
		Result(User)
		Error("unauthorized")

		HTTP(func() {
			GET("/users/me")
			Response(StatusOK)
			Response("unauthorized", StatusUnauthorized)
		})
	})
})

// tagName はタグ名の制約。EntryRequest の tags の要素と、tags.update の name で共有する。
// 前後の空白は許さない（"go" と "go " が別のタグになるのを防ぐ）。
func tagName() {
	MinLength(1)
	MaxLength(50)
	Pattern(`^\S(.*\S)?$`)
}

// EntryRequest はクライアントが書き込めるフィールドだけを持つリクエスト型。
// id / created_at / updated_at はサーバー側で付与する。
// user_id は JWT から引くので、クライアントからは受け取らない。
var EntryRequest = Type("EntryRequest", func() {
	Description("Client-writable fields of a journal entry")

	Attribute("entry_date", String, "記録日", func() {
		Format(FormatDate)
	})
	Attribute("kind", String, func() {
		Enum("til", "diary")
	})
	// Required はキーの存在しか見ないため、空文字を弾くには MinLength が要る
	Attribute("title", String, func() {
		MinLength(1)
	})
	Attribute("body", String)
	// タグは名前で受け取る。無いものはそのユーザのタグとして作られ、あるものは
	// 紐付けだけ増える（tags サービスの list で id が分かる）。PUT では全置換なので、
	// 省略すると全部外れる。
	Attribute("tags", ArrayOf(String, tagName), "タグ名の一覧", func() {
		MaxLength(20)
		Example([]string{"go", "goa"})
	})

	Required("title", "entry_date", "kind")
})

// Journal は API が返すエントリの形。EntryRequest + サーバー管理フィールド。
var Journal = Type("Journal", func() {
	Description("A journal entry")

	Extend(EntryRequest)

	// id は自動採番。タイムスタンプもサーバー側で付与する
	Attribute("id", Int64)
	Attribute("created_at", String, func() {
		Format(FormatDateTime)
	})
	Attribute("updated_at", String, func() {
		Format(FormatDateTime)
	})
	// user_id は作成時に JWT の sub から入れる（users.id）。DB では NOT NULL。
	Attribute("user_id", Int64)

	// レスポンスでは tags を必ず出す（無ければ []）。EntryRequest では任意だが、
	// ここで Required にしないと生成コードが omitempty を付け、空のときにキーごと消える。
	Required("tags")
})

// Service entries: 5 メソッド(POST / GET list / GET one / PUT / DELETE)。
//
// どのメソッドも認証したユーザの記録だけを扱う。一覧は自分のものだけを返し、
// id を指定する get / update / delete は他人の id を「存在しない」と同じ not_found にする
// （403 にすると、その id が存在することが外から分かる）。
var _ = Service("entries", func() {
	Description("Journal entries of the authenticated user")

	// 全メソッドで JWT を要求する。誰の記録かをトークンから決めるため。
	Security(JWTAuth)
	// トークンが無い・壊れている・期限切れのときは 401。全メソッド共通なので
	// サービスに置き、HTTP のマッピングも 1 箇所にまとめる。
	Error("unauthorized")
	HTTP(func() {
		Response("unauthorized", StatusUnauthorized)
	})

	Method("create", func() {
		Description("Create a new journal entry owned by the authenticated user")
		// EntryRequest + トークン。トークンは Authorization ヘッダに載るので、
		// リクエストボディは EntryRequest のフィールドだけのまま。
		Payload(func() {
			Extend(EntryRequest)
			jwtToken()
		})
		// Journal + location。location は Location ヘッダにマップされるため、
		// レスポンスボディには Journal のフィールドだけが残る
		Result(func() {
			Extend(Journal)
			Attribute("location", String, "作成されたリソースのパス", func() {
				Example("/entries/1")
			})
			Required("location")
		})

		HTTP(func() {
			POST("/entries")
			Response(StatusCreated, func() {
				Header("location:Location")
			})
		})
	})

	Method("list", func() {
		Description("List the authenticated user's journal entries")
		// 無条件に全件返すと、件数が増えるほど1リクエストが重くなる。
		// 既定は 10 件で、クエリパラメータでずらして読む。
		Payload(func() {
			Attribute("limit", Int, "取得する件数", func() {
				Minimum(1)
				Maximum(100)
				Default(10)
			})
			Attribute("offset", Int, "先頭から読み飛ばす件数", func() {
				Minimum(0)
				Default(0)
			})
			jwtToken()
		})
		Result(ArrayOf(Journal))

		HTTP(func() {
			GET("/entries")
			Param("limit")
			Param("offset")
			Response(StatusOK)
		})
	})

	Method("get", func() {
		Description("Get a journal entry by ID (entries of other users are not found)")
		// 無名の Payload(Int64) だと CLI のフラグが -p になるので、id と名前を付ける
		Payload(func() {
			Attribute("id", Int64, "Entry ID")
			Required("id")
			jwtToken()
		})
		Result(Journal)
		// id が存在しない、または他人のものなら 404
		Error("not_found")
		HTTP(func() {
			GET("/entries/{id}")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
		})
	})

	Method("update", func() {
		Description("Update a journal entry by ID (entries of other users are not found)")
		// EntryRequest + パスパラメータの id。id は必須なので実装側で nil チェックが要らない
		Payload(func() {
			Extend(EntryRequest)
			Attribute("id", Int64, "Entry ID")
			Required("id")
			jwtToken()
		})
		Result(Journal)

		// id が存在しない、または他人のものなら 404
		Error("not_found")
		HTTP(func() {
			PUT("/entries/{id}")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
		})
	})

	Method("delete", func() {
		Description("Delete a journal entry by ID (entries of other users are not found)")
		Payload(func() {
			Attribute("id", Int64, "Entry ID")
			Required("id")
			jwtToken()
		})

		// id が存在しない、または他人のものなら 404
		Error("not_found")
		HTTP(func() {
			DELETE("/entries/{id}")
			Response(StatusNoContent)
			Response("not_found", StatusNotFound)
		})
	})
})

// TagResult は API が返すタグ（型名は Tag。Go の変数名は DSL の Tag 関数と衝突するので別にしている）。
// entry_count は紐付いている記録の数で、0 のタグも残る（消したければ tags.delete）。
var TagResult = Type("Tag", func() {
	Description("A tag owned by the authenticated user")

	Attribute("id", Int64)
	Attribute("name", String, tagName)
	Attribute("entry_count", Int, "このタグが付いている記録の数")

	Required("id", "name", "entry_count")
})

// Service tags: 一覧 / 改名 / 削除。
//
// 作成は持たない。タグは entries の tags に名前を書いたときに暗黙に作られるので、
// POST /tags を用意しても「名前だけのタグ」を作る用途しか無い。
// 改名は紐付いている全ての記録に効き、削除は紐付けごと消す（記録は残る）。
var _ = Service("tags", func() {
	Description("Tags of the authenticated user")

	Security(JWTAuth)
	Error("unauthorized")
	HTTP(func() {
		Response("unauthorized", StatusUnauthorized)
	})

	Method("list", func() {
		Description("List the authenticated user's tags with entry counts, sorted by name")
		Payload(jwtToken)
		Result(ArrayOf(TagResult))

		HTTP(func() {
			GET("/tags")
			Response(StatusOK)
		})
	})

	Method("update", func() {
		Description("Rename a tag (applies to every entry carrying it)")
		Payload(func() {
			Attribute("id", Int64, "Tag ID")
			Attribute("name", String, "新しい名前", tagName)
			Required("id", "name")
			jwtToken()
		})
		Result(TagResult)
		// id が存在しない、または他人のものなら 404（entries と同じ扱い）
		Error("not_found")
		// 同じ名前のタグが既にあれば 409。統合はしない（記録の付け替えは entries 側でやる）
		Error("conflict")

		HTTP(func() {
			PUT("/tags/{id}")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
			Response("conflict", StatusConflict)
		})
	})

	Method("delete", func() {
		Description("Delete a tag and detach it from every entry")
		Payload(func() {
			Attribute("id", Int64, "Tag ID")
			Required("id")
			jwtToken()
		})
		Error("not_found")

		HTTP(func() {
			DELETE("/tags/{id}")
			Response(StatusNoContent)
			Response("not_found", StatusNotFound)
		})
	})
})
