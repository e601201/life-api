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
