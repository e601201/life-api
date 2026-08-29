package life

import (
	"context"

	health "github.com/e601201/life-api/gen/health"
	"goa.design/clue/log"
)

// health service example implementation.
// The example methods log the requests and return zero values.
type healthsrvc struct{}

// NewHealth returns the health service implementation.
func NewHealth() health.Service {
	return &healthsrvc{}
}

// Return OK if the server is alive.
func (s *healthsrvc) Check(ctx context.Context) (res string, err error) {
	log.Printf(ctx, "health.check")
	return "server OK!", nil
}
