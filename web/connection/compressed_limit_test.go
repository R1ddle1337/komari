package connection

import (
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompressedFrameCannotBypassReadLimit(t *testing.T) {
	result := make(chan error, 1)
	upgrader := websocket.Upgrader{EnableCompression: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		sc := NewSafeConn(raw)
		sc.SetReadLimit(128)
		defer sc.Close()
		_, _, err = sc.ReadMessage()
		result <- err
	}))
	defer server.Close()
	dialer := websocket.Dialer{EnableCompression: true}
	client, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 1024))); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if !errors.Is(err, websocket.ErrReadLimit) {
			t.Fatalf("decompressed message escaped limit: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("oversized compressed frame stalled reader")
	}
}
