package server

import (
	"context"
	"net/http"
	"time"
)

// contextWithTimeout bounds a request-scoped operation.
//
// Derived from the request context so a browser that navigates away cancels the
// query it started, instead of leaving the panel to finish work nobody is
// waiting for.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
