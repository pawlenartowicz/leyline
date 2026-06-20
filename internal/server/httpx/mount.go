package httpx

import (
	"net/http"
	"time"
)

// Mounter binds method+path patterns onto a ServeMux, applying a uniform
// auth + timeout chain per route. cap is `any` so httpx doesn't import
// internal/caps (avoids cycle); callers pass their typed capability and
// the auth closure casts internally.
type Mounter struct {
	Mux            *http.ServeMux
	Prefix         string
	Auth           func(needed any) func(http.Handler) http.Handler
	DefaultTimeout time.Duration // 0 = no timeout
}

type handleOpts struct {
	timeout    time.Duration
	timeoutSet bool
}

type HandleOption func(*handleOpts)

// WithTimeout overrides Mounter.DefaultTimeout for a single route. Pass 0
// to disable the timeout entirely for this route.
func WithTimeout(d time.Duration) HandleOption {
	return func(o *handleOpts) {
		o.timeout = d
		o.timeoutSet = true
	}
}

// Handle registers a route under Mounter.Prefix with the given method.
// Layering: Auth wraps Timeout wraps handler. Auth runs first so any body-
// size cap inside the auth middleware applies before the timeout window
// opens. A nil cap binds the handler without auth wrapping.
func (m Mounter) Handle(method, path string, cap any, h http.HandlerFunc, opts ...HandleOption) {
	o := handleOpts{timeout: m.DefaultTimeout}
	for _, fn := range opts {
		fn(&o)
	}
	var handler http.Handler = h
	if o.timeout > 0 {
		handler = Timeout(o.timeout)(handler)
	}
	if cap != nil {
		handler = m.Auth(cap)(handler)
	}
	m.Mux.Handle(method+" "+m.Prefix+path, handler)
}
