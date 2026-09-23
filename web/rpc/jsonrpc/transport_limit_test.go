package jsonrpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRPCRejectsOversizedBodyAndBatchBeforeDispatch(t *testing.T) {
	for _, body := range []string{
		strings.Repeat(" ", maxRPCRequestBytes+1),
		"[" + strings.Repeat(`{"jsonrpc":"2.0","method":"rpc.ping","id":1},`, maxRPCBatchRequests) + `{"jsonrpc":"2.0","method":"rpc.ping","id":1}]`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/rpc2", strings.NewReader(body))
		servePost(c)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
}

func TestSingleEntryRPCBatchKeepsArrayResponse(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/rpc2", strings.NewReader(`[{"jsonrpc":"2.0","method":"rpc.ping","id":1}]`))
	servePost(c)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "[") {
		t.Fatalf("batch response: %d %s", w.Code, w.Body.String())
	}
}
