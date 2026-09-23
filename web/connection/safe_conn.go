package connection

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ConnInfo describes one WebSocket connection passed to interceptor
// callbacks. ID matches the owning SafeConn.
type ConnInfo struct {
	ID         int64
	Path       string
	RemoteIP   string
	UserAgent  string
	ClientUUID string
}

// FrameInterceptor is the optional hook chain around WebSocket connections
// and frames. The provider is wired once at startup by internal/server (the
// plugin manager); a nil interceptor leaves every connection untouched.
type FrameInterceptor interface {
	// OnConnect runs right after the upgrade. A denied connection is closed
	// by the caller; plugins bear the consequences of rejecting endpoints
	// such as /api/rpc2.
	OnConnect(info *ConnInfo) (deny bool, reason string)
	// OnMessage inspects one inbound frame; OnSend one outbound frame. They
	// return the possibly replaced payload and whether the frame is dropped.
	OnMessage(info *ConnInfo, frameType int, data []byte) (newType int, newData []byte, drop bool)
	OnSend(info *ConnInfo, frameType int, data []byte) (newType int, newData []byte, drop bool)
	// OnClose notifies that the connection ended.
	OnClose(info *ConnInfo)
}

// frameInterceptor is the process-wide WebSocket hook provider.
var frameInterceptor FrameInterceptor

// SetFrameInterceptor wires the plugin hook chain into every WebSocket
// connection. It must be called once at startup, before connections arrive.
func SetFrameInterceptor(i FrameInterceptor) { frameInterceptor = i }

// Interceptor returns the process-wide frame interceptor, or nil when no
// provider is wired.
func Interceptor() FrameInterceptor { return frameInterceptor }

// errTooManyDroppedFrames fails the read loop after a plugin dropped too many
// frames in a row, so a connection on a fully filtered endpoint cannot spin
// forever.
var errTooManyDroppedFrames = errors.New("too many frames dropped by plugin hooks")

// maxConsecutiveDroppedFrames bounds the internal re-read loop.
const maxConsecutiveDroppedFrames = 16

const defaultWriteTimeout = 10 * time.Second

// SafeConn wraps a websocket connection with a mutex so concurrent writes
// cannot interleave frames, and funnels every frame through the optional
// plugin interceptor when one is attached.
type SafeConn struct {
	conn         *websocket.Conn
	mu           sync.Mutex
	ID           int64
	writeMu      sync.Mutex
	writeTimeout time.Duration
	closeOnce    sync.Once
	closeErr     error
	readLimit    int64

	interceptor FrameInterceptor
	info        *ConnInfo
}

func NewSafeConn(conn *websocket.Conn) *SafeConn {
	return &SafeConn{
		conn:         conn,
		mu:           sync.Mutex{},
		ID:           time.Now().UnixNano(),
		writeTimeout: defaultWriteTimeout,
	}
}

// SetInterceptor attaches the hook provider and the connection identity. It
// must be called before any frame is read or written.
func (sc *SafeConn) SetInterceptor(info *ConnInfo, interceptor FrameInterceptor) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.info = info
	sc.interceptor = interceptor
}

// readFrame reads one frame and runs the wsMessage hook chain. Frames
// dropped by hooks are transparently skipped, so callers only ever see
// processed frames.
func (sc *SafeConn) readFrame() (int, []byte, error) {
	for dropped := 0; ; {
		messageType, reader, err := sc.conn.NextReader()
		if err != nil {
			return 0, nil, err
		}
		if sc.readLimit > 0 {
			reader = io.LimitReader(reader, sc.readLimit+1)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return 0, nil, err
		}
		if sc.readLimit > 0 && int64(len(data)) > sc.readLimit {
			_ = sc.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message too large"), time.Now().Add(time.Second))
			return 0, nil, websocket.ErrReadLimit
		}
		sc.mu.Lock()
		interceptor, info := sc.interceptor, sc.info
		sc.mu.Unlock()
		if interceptor == nil || info == nil {
			return messageType, data, nil
		}
		newType, newData, drop := interceptor.OnMessage(info, messageType, data)
		if !drop {
			return newType, newData, nil
		}
		if dropped++; dropped >= maxConsecutiveDroppedFrames {
			return 0, nil, errTooManyDroppedFrames
		}
	}
}

// writeFrame runs the wsSend hook chain then writes one frame. A dropped
// frame is silently skipped; a nil interceptor writes through immediately.
func (sc *SafeConn) writeFrame(messageType int, data []byte) error {
	sc.writeMu.Lock()
	defer sc.writeMu.Unlock()
	sc.mu.Lock()
	interceptor, info := sc.interceptor, sc.info
	sc.mu.Unlock()
	if interceptor != nil && info != nil {
		newType, newData, drop := interceptor.OnSend(info, messageType, data)
		if drop {
			return nil
		}
		messageType, data = newType, newData
	}
	if err := sc.conn.SetWriteDeadline(time.Now().Add(sc.writeTimeout)); err != nil {
		return err
	}
	return sc.conn.WriteMessage(messageType, data)
}

func (sc *SafeConn) WriteMessage(messageType int, data []byte) error {
	return sc.writeFrame(messageType, data)
}

// WriteJSON encodes v then sends it as one text frame through the wsSend
// hook chain.
func (sc *SafeConn) WriteJSON(v interface{}) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return err
	}
	return sc.writeFrame(websocket.TextMessage, buf.Bytes())
}

func (sc *SafeConn) Close() error {
	var interceptor FrameInterceptor
	var info *ConnInfo
	sc.closeOnce.Do(func() {
		// gorilla allows Close concurrently with all operations. Closing first
		// interrupts a blocked writer instead of waiting behind its mutex.
		sc.closeErr = sc.conn.Close()
		sc.mu.Lock()
		interceptor, info = sc.interceptor, sc.info
		sc.interceptor = nil
		sc.mu.Unlock()
	})
	// Run user callbacks outside both the state lock and sync.Once so hooks
	// can safely reenter Close or inspect the connection.
	if interceptor != nil && info != nil {
		interceptor.OnClose(info)
	}
	return sc.closeErr
}

func (sc *SafeConn) ReadMessage() (int, []byte, error) {
	return sc.readFrame()
}

func (sc *SafeConn) ReadJSON(v interface{}) error {
	_, data, err := sc.readFrame()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (sc *SafeConn) SetReadDeadline(t time.Time) error {
	return sc.conn.SetReadDeadline(t)
}

// SetReadLimit must be called before the read loop. Bound the decompressed
// message too: gorilla's own limit applies to compressed frame bytes.
func (sc *SafeConn) SetReadLimit(limit int64) {
	sc.readLimit = limit
	sc.conn.SetReadLimit(limit)
}

func (sc *SafeConn) GetConn() *websocket.Conn {
	return sc.conn
}

// SetCloseHandler forwards to the underlying connection (used by the
// terminal sessions to clean up on close).
func (sc *SafeConn) SetCloseHandler(h func(code int, text string) error) {
	sc.conn.SetCloseHandler(h)
}
