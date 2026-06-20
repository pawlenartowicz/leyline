package httpx

import (
	"net/http"
	"time"
)

// Timeout wraps a handler with http.TimeoutHandler, returning a 503 JSON body
// if the handler exceeds d. The underlying goroutine is NOT cancelled — work
// continues to completion in the background. The response body is buffered
// until handler return; streaming endpoints must opt out via WithTimeout(0).
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, d, `{"error":"request timed out"}`)
	}
}
