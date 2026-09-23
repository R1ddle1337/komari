package connection

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type observedWriteConn struct {
	net.Conn
	watch   atomic.Bool
	once    sync.Once
	writing chan struct{}
}

func (c *observedWriteConn) Write(p []byte) (int, error) {
	if c.watch.Load() {
		c.once.Do(func() { close(c.writing) })
	}
	return c.Conn.Write(p)
}

// Complete a real handshake, then stop reading. net.Pipe gives deterministic
// backpressure and real deadline/Close behavior without filling OS buffers.
func stalledPeer(t *testing.T) (*SafeConn, <-chan struct{}) {
	t.Helper()
	raw, peer := net.Pipe()
	conn := &observedWriteConn{Conn: raw, writing: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer peer.Close()
		request, err := http.ReadRequest(bufio.NewReader(peer))
		if err != nil {
			return
		}
		digest := sha1.Sum([]byte(request.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = fmt.Fprintf(peer, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(digest[:]))
		<-done
	}()
	dialer := websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }, HandshakeTimeout: time.Second}
	ws, _, err := dialer.Dial("ws://stalled.test", nil)
	if err != nil {
		close(done)
		raw.Close()
		t.Fatal(err)
	}
	conn.watch.Store(true)
	safe := NewSafeConn(ws)
	t.Cleanup(func() { _ = raw.Close(); close(done) })
	return safe, conn.writing
}

func TestCloseInterruptsBlockedWrite(t *testing.T) {
	sc, writing := stalledPeer(t)
	writeDone := make(chan error, 1)
	go func() { writeDone <- sc.WriteMessage(websocket.TextMessage, []byte("report")) }()
	select {
	case <-writing:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	closed := make(chan struct{})
	go func() { _ = sc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close waited behind a blocked write")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("write to a closed connection succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write was not interrupted")
	}
}

func TestStalledPeerWriteTimesOut(t *testing.T) {
	for _, jsonFrame := range []bool{false, true} {
		t.Run(fmt.Sprint("json=", jsonFrame), func(t *testing.T) {
			sc, _ := stalledPeer(t)
			sc.writeTimeout = 30 * time.Millisecond
			done := make(chan error, 1)
			go func() {
				if jsonFrame {
					done <- sc.WriteJSON(map[string]string{"status": "ok"})
				} else {
					done <- sc.WriteMessage(websocket.TextMessage, []byte("status"))
				}
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled peer did not time out")
				}
				if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
					t.Fatalf("expected timeout, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("write exceeded deadline")
			}
		})
	}
}

type selfClosingInterceptor struct {
	recordingInterceptor
	conn *SafeConn
}

func (i *selfClosingInterceptor) OnClose(info *ConnInfo) {
	i.recordingInterceptor.OnClose(info)
	_ = i.conn.Close()
}

func TestCloseHookMayReenterAndRunsOnce(t *testing.T) {
	sc, _ := stalledPeer(t)
	hook := &selfClosingInterceptor{conn: sc}
	sc.SetInterceptor(&ConnInfo{ID: sc.ID}, hook)
	done := make(chan struct{})
	go func() {
		var group sync.WaitGroup
		for range 10 {
			group.Add(1)
			go func() { defer group.Done(); _ = sc.Close() }()
		}
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant close hook deadlocked")
	}
	_, _, _, closes := hook.calls()
	if closes != 1 {
		t.Fatalf("close hook ran %d times", closes)
	}
}
