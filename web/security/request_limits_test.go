package security

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestLoginBodyIsBoundedBeforeInnerHooks(t *testing.T) {
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	var received int
	GuardLoginBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		received = len(body)
		var limit *http.MaxBytesError
		if !errors.As(err, &limit) {
			t.Fatalf("expected bounded reader before handler, got %v", err)
		}
	})).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(strings.Repeat("x", MaxLoginBodyBytes+1))))
	if received != MaxLoginBodyBytes || len(w.deadlines) != 2 || w.deadlines[0].IsZero() || !w.deadlines[1].IsZero() {
		t.Fatal("login guard did not bound and restore the request")
	}
	GuardLoginBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != MaxLoginBodyBytes+1 {
			t.Fatal("streaming transfer changed")
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/clients/files/stream", strings.NewReader(strings.Repeat("x", MaxLoginBodyBytes+1))))
}
