package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	"github.com/komari-monitor/komari/web/connection"
)

func TestLegacyCannotReplaceActiveV2ConnectionOrReport(t *testing.T) {
	uuid := t.Name()
	current := &connection.SafeConn{ID: 101}
	late := &connection.SafeConn{ID: 102}
	t.Cleanup(func() { DeleteConnectedClients(uuid); DeleteLatestReport(uuid) })
	RegisterConnectedClient(uuid, current, 2)
	RecordReport(v2.Report{UUID: uuid, CPU: v2.CPUReport{Usage: 42}})
	if previous, accepted := RegisterConnectedClient(uuid, late, 1); accepted || previous != nil {
		t.Fatal("legacy connection replaced active v2")
	}
	if RecordLegacyReport(v2.Report{UUID: uuid, CPU: v2.CPUReport{Usage: 99}}) {
		t.Fatal("legacy report replaced active v2 state")
	}
	if KeepAliveLegacyPresence(uuid, late.ID, time.Minute) {
		t.Fatal("legacy POST replaced active v2 presence")
	}
	DeleteClientConditionally(uuid, late)
	if !IsCurrentClientConnection(uuid, current) || !IsV2Client(uuid) {
		t.Fatal("late legacy cleanup removed the current v2 connection")
	}
	if got := GetLatestReport()[uuid]; got == nil || got.CPU.Usage != 42 {
		t.Fatalf("v2 report cache changed: %#v", got)
	}
}

func TestLegacyMayReconnectAfterV2PresenceExpires(t *testing.T) {
	uuid := t.Name()
	t.Cleanup(func() { DeleteConnectedClients(uuid); SetPresence(uuid, 10, false) })
	KeepAlivePresence(uuid, 10, -time.Second)
	MarkV2Client(uuid)
	legacy := &connection.SafeConn{ID: 11}
	if _, accepted := RegisterConnectedClient(uuid, legacy, 1); !accepted || IsV2Client(uuid) {
		t.Fatal("expired v2 presence prevented a legitimate v1 reconnect")
	}
}

func TestLegacyDispatchAndV2FileEventsUseCorrectWebSocketShape(t *testing.T) {
	accepted := make(chan *connection.SafeConn, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- connection.NewSafeConn(conn)
		<-release
	}))
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	conn := <-accepted
	uuid := t.Name()
	t.Cleanup(func() {
		DeleteConnectedClients(uuid)
		close(release)
		ws.Close()
		conn.Close()
		server.Close()
	})
	RegisterConnectedClient(uuid, conn, 1)
	// 晚到的 v2 HTTP 能力标记不能改变已经建立的 v1 WebSocket 信封。
	MarkV2Client(uuid)
	if IsV2Client(uuid) {
		t.Fatal("late v2 marker changed the active WebSocket protocol")
	}
	cases := []struct {
		method  string
		params  any
		message string
		key     string
		value   any
	}{
		{v2.MethodAgentExec, v2.ExecParams{TaskID: "task-1", Command: "true"}, "exec", "task_id", "task-1"},
		{v2.MethodAgentPing, v2.PingParams{TaskID: 7, Type: "tcp", Target: "127.0.0.1:22"}, "ping", "ping_task_id", float64(7)},
		{v2.MethodAgentTerminal, v2.TerminalRequestParams{RequestID: "terminal-1"}, "terminal", "request_id", "terminal-1"},
	}
	for _, tc := range cases {
		if !DispatchV2Event(uuid, tc.method, tc.params) {
			t.Fatalf("dispatch failed: %s", tc.method)
		}
		ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		var got map[string]any
		if err := ws.ReadJSON(&got); err != nil {
			t.Fatal(err)
		}
		if got["jsonrpc"] != nil || got["message"] != tc.message || got[tc.key] != tc.value {
			t.Fatalf("unexpected v1 payload: %#v", got)
		}
	}
	if DispatchV2Event(uuid, v2.MethodAgentFile, v2.FileOperation{Op: "list"}) {
		t.Fatal("v1 client accepted a v2-only file operation")
	}
	RegisterConnectedClient(uuid, conn, 2)
	if !DispatchV2Event(uuid, v2.MethodAgentFile, v2.FileOperation{Op: "list"}) {
		t.Fatal("v2 file operation stopped working")
	}
	var got v2.Request
	if err := ws.ReadJSON(&got); err != nil {
		t.Fatal(err)
	}
	if got.JSONRPC != v2.Version || got.Method != v2.MethodAgentFile {
		t.Fatalf("unexpected v2 file payload: %#v", got)
	}
}
