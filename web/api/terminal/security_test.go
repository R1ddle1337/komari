package terminal

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/web/connection"
)

func TestMain(m *testing.M) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:komari_terminal_test?mode=memory&cache=shared"
	dbcore.GetDBInstance()
	os.Exit(m.Run())
}

func terminalTestPair(t *testing.T) (*websocket.Conn, *connection.SafeConn) {
	t.Helper()
	accepted := make(chan *connection.SafeConn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			accepted <- connection.NewSafeConn(conn)
		}
	}))
	t.Cleanup(s.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	select {
	case server := <-accepted:
		t.Cleanup(func() { server.Close() })
		return client, server
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket upgrade timed out")
		return nil, nil
	}
}

func TestTerminalRevocationBlocksInputAndClosesIdleSessions(t *testing.T) {
	for _, kind := range []string{"browser_input", "idle_agent"} {
		t.Run(kind, func(t *testing.T) {
			browserClient, browser := terminalTestPair(t)
			agentClient, agent := terminalTestPair(t)
			var browserValid, agentValid atomic.Bool
			browserValid.Store(true)
			agentValid.Store(true)
			id := t.Name()
			TerminalSessionsMutex.Lock()
			TerminalSessions[id] = &TerminalSession{
				Browser: browser, Agent: agent, RequesterIp: "127.0.0.1",
				BrowserCredentialsValid: browserValid.Load, AgentCredentialsValid: agentValid.Load,
			}
			TerminalSessionsMutex.Unlock()
			t.Cleanup(func() { closeSession(id) })
			done := make(chan struct{})
			go func() { ForwardTerminal(id, browser, agent); close(done) }()
			if err := browserClient.WriteMessage(websocket.BinaryMessage, []byte("allowed-input")); err != nil {
				t.Fatal(err)
			}
			agentClient.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, payload, err := agentClient.ReadMessage()
			if err != nil || string(payload) != "allowed-input" {
				t.Fatalf("valid input failed: %q %v", payload, err)
			}
			if kind == "browser_input" {
				browserValid.Store(false)
				if err := browserClient.WriteMessage(websocket.BinaryMessage, []byte("revoked-input")); err != nil {
					t.Fatal(err)
				}
			} else {
				agentValid.Store(false)
			}
			agentClient.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, data, err := agentClient.ReadMessage(); err == nil {
				t.Fatalf("revoked session forwarded %q", data)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("revoked forwarding did not stop")
			}
			TerminalSessionsMutex.Lock()
			_, exists := TerminalSessions[id]
			TerminalSessionsMutex.Unlock()
			if exists {
				t.Fatal("revoked terminal remains reattachable")
			}
		})
	}
}

func TestStaleTerminalCannotCloseReplacementConnection(t *testing.T) {
	_, oldBrowser := terminalTestPair(t)
	_, replacement := terminalTestPair(t)
	_, agent := terminalTestPair(t)
	id := t.Name()
	TerminalSessionsMutex.Lock()
	TerminalSessions[id] = &TerminalSession{Browser: replacement, Agent: agent}
	TerminalSessionsMutex.Unlock()
	t.Cleanup(func() { closeSession(id) })
	closeSessionIfCurrent(id, oldBrowser, agent)
	TerminalSessionsMutex.Lock()
	session := TerminalSessions[id]
	TerminalSessionsMutex.Unlock()
	if session == nil || session.Browser != replacement {
		t.Fatal("stale forwarding closed replacement")
	}
}
