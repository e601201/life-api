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

// Journal type definition
var Journal = Type("Journal", func() {
	Description("A journal entry")

	// id / entry_date(記録日) / kind（til or diary）/ title / body / created_at / updated_at
	// tags 現状では入れない
	// idは自動採番されるので、クライアントからは送信されない
	// user_idは今のうちに入れておく。
	Attribute("id", Int64)
	Attribute("entry_date", String)
	Attribute("kind", String, func() {
		Enum("til", "diary")
	})
	Attribute("title", String)
	Attribute("body", String)
	Attribute("created_at", String)
	Attribute("updated_at", String)
	Attribute("user_id", Int64)

	Required("title", "entry_date", "kind")
})

// JournalにService entries に 5 メソッド(POST / GET list / GET one / PUT / DELETE)。
var _ = Service("entries", func() {
	Description("Journal entries service")

	Method("create", func() {
		Description("Create a new journal entry")
		Payload(Journal)
		Result(Journal)

		HTTP(func() {
			POST("/entries")
			Response(StatusCreated)
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
		Payload(Int64)
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
		Payload(Journal)
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
		Payload(Int64)

		// idが存在しない場合は404を返す
		Error("not_found")
		HTTP(func() {
			DELETE("/entries/{id}")
			Response(StatusNoContent)
			Response("not_found", StatusNotFound)
		})
	})
})
