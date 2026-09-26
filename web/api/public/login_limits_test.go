package public

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/utils"
)

type heldLoginBody struct {
	started chan struct{}
	release chan struct{}
	read    bool
}

func (b *heldLoginBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	close(b.started)
	<-b.release
	return copy(p, "{}"), io.EOF
}
func (b *heldLoginBody) Close() error { return nil }

func TestSlowLoginBodyDoesNotReservePasswordMemory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/api/login", Login)
	body := &heldLoginBody{started: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "/api/login", body)
	request.RemoteAddr = "203.0.113.198:1234"
	done := make(chan struct{})
	go func() { engine.ServeHTTP(httptest.NewRecorder(), request); close(done) }()
	<-body.started
	reserved := len(passwordLoginSlots)
	close(body.release)
	<-done
	if reserved != 0 {
		t.Fatal("unfinished login body occupied a password-work slot")
	}
}

func TestLoginRateLimitBoundsSourcesAndExpires(t *testing.T) {
	limiter := &loginLimiter{sources: make(map[string]loginWindowState)}
	now := time.Now()
	for range loginPerIP {
		if !limiter.allow("2001:db8:1::1", now) {
			t.Fatal("early refusal")
		}
	}
	if limiter.allow("2001:db8:1::2", now) {
		t.Fatal("IPv6 privacy address bypassed per-network limit")
	}
	if !limiter.allow("192.0.2.1", now) {
		t.Fatal("unrelated source refused")
	}
	if !limiter.allow("2001:db8:1::1", now.Add(loginWindow)) {
		t.Fatal("limit did not expire")
	}
	for i := range maxLoginSources {
		limiter.sources[string(rune(i))] = loginWindowState{start: now, attempts: 1}
	}
	if limiter.allow("another-source", now) {
		t.Fatal("source map grew past its bound")
	}
	if !limiter.allow("another-source", now.Add(loginWindow)) {
		t.Fatal("expired source entries were not reclaimed")
	}
}

func TestLoginLimitsBodyAndRejectsSpoofedForwarding(t *testing.T) {
	t.Setenv("KOMARI_TRUSTED_PROXIES", "127.0.0.1")
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if err := utils.ConfigureTrustedProxies(engine); err != nil {
		t.Fatal(err)
	}
	engine.POST("/api/login", Login)
	send := func(body, forwarded string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
		request.RemoteAddr = "203.0.113.199:1234"
		request.Header.Set("X-Forwarded-For", forwarded)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		return response
	}
	if response := send(strings.Repeat("x", maxLoginBytes+1), "198.51.100.1"); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized login status %d", response.Code)
	}
	for i := 1; i < loginPerIP; i++ {
		if response := send(`{}`, "198.51.100.2"); response.Code != http.StatusBadRequest {
			t.Fatalf("expected bounded invalid request, got %d", response.Code)
		}
	}
	if response := send(`{}`, "198.51.100.3"); response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatal("forwarded header bypassed login limit")
	}
}
