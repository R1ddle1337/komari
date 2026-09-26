package plugin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type controlledWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *controlledWriter) SetReadDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestPluginWritersPreserveResponseControllerDeadlines(t *testing.T) {
	base := &controlledWriter{ResponseRecorder: httptest.NewRecorder()}
	wrapped := newBufferedResponseWriter(newHTMLInjectWriter(base))
	deadline := time.Now().Add(time.Second)
	if err := http.NewResponseController(wrapped).SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if !base.deadline.Equal(deadline) {
		t.Fatal("plugin wrapper swallowed the read deadline")
	}
}
