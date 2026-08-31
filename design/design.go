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

var _ = Service("health", func() {
	Description("health check this server")

	Method("check", func() {
		Description("Return OK if the server is alive.")
		Result(String, "OK")

		HTTP(func() {
			GET("/health")
		})
	})
})

// EntryRequest はクライアントが書き込めるフィールドだけを持つリクエスト型。
// id / created_at / updated_at はサーバー側で付与する。
// user_id も W3 で JWT から入れる予定のため、クライアントからは受け取らない。
// tags は現状では入れない
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
	// user_id は W3 のマルチユーザ化を見越して先に持たせておく（値は JWT 導入時に入る）
	Attribute("user_id", Int64)
})

// Service entries: 5 メソッド(POST / GET list / GET one / PUT / DELETE)。
var _ = Service("entries", func() {
	Description("Journal entries service")

	Method("create", func() {
		Description("Create a new journal entry")
		Payload(EntryRequest)
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
		Description("List all journal entries")
		Result(ArrayOf(Journal))

		HTTP(func() {
			GET("/entries")
			Response(StatusOK)
		})
	})

	Method("get", func() {
		Description("Get a journal entry by ID")
		// 無名の Payload(Int64) だと CLI のフラグが -p になるので、id と名前を付ける
		Payload(func() {
			Attribute("id", Int64, "Entry ID")
			Required("id")
		})
		Result(Journal)
		// idが存在しない場合は404を返す
		Error("not_found")
		HTTP(func() {
			GET("/entries/{id}")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
		})
	})

	Method("update", func() {
		Description("Update a journal entry by ID")
		// EntryRequest + パスパラメータの id。id は必須なので実装側で nil チェックが要らない
		Payload(func() {
			Extend(EntryRequest)
			Attribute("id", Int64, "Entry ID")
			Required("id")
		})
		Result(Journal)

		// idが存在しない場合は404を返す
		Error("not_found")
		HTTP(func() {
			PUT("/entries/{id}")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
		})
	})

	Method("delete", func() {
		Description("Delete a journal entry by ID")
		Payload(func() {
			Attribute("id", Int64, "Entry ID")
			Required("id")
		})

		// idが存在しない場合は404を返す
		Error("not_found")
		HTTP(func() {
			DELETE("/entries/{id}")
			Response(StatusNoContent)
			Response("not_found", StatusNotFound)
		})
	})
})
