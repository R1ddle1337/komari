package security

import (
	"net/http"
	"time"
)

const MaxLoginBodyBytes = 16 << 10
const LoginBodyTimeout = 10 * time.Second

// GuardLoginBody sits outside plugin request hooks, which can read bodies before
// the router runs. It leaves streaming transfers and other endpoints unchanged.
func GuardLoginBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/login" {
			control := http.NewResponseController(w)
			_ = control.SetReadDeadline(time.Now().Add(LoginBodyTimeout))
			defer control.SetReadDeadline(time.Time{})
			r.Body = http.MaxBytesReader(w, r.Body, MaxLoginBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}
