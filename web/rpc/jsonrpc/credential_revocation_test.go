package jsonrpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/config"
	"github.com/komari-monitor/komari/pkg/rpc"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	"github.com/komari-monitor/komari/web/api"
	clientapi "github.com/komari-monitor/komari/web/api/client"
	"github.com/pquerna/otp/totp"
)

func revocationTestSession(t *testing.T) (models.User, string) {
	t.Helper()
	u, err := accounts.CreateAccount(t.Name(), "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accounts.DeleteAccountByUsername(u.Username) })
	session, err := accounts.CreateSession(u.UUID, 600, "test", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accounts.DeleteSession(session) })
	return u, session
}

func dialRevocationTestRPC(t *testing.T, headers http.Header, query string) *websocket.Conn {
	t.Helper()
	r := gin.New()
	r.Use(api.IdentityMiddleware())
	r.GET("/api/rpc2", OnRpcRequest)
	s := httptest.NewServer(r)
	t.Cleanup(s.Close)
	headers.Set("Origin", s.URL)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/api/rpc2"+query, headers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func revocationTestCall(t *testing.T, conn *websocket.Conn, method string, params any) rpc.JsonRpcResponse {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.WriteJSON(rpc.JsonRpcRequest{Version: rpc.RPC_VERSION, ID: 1, Method: method, Params: params}); err != nil {
		t.Fatal(err)
	}
	var response rpc.JsonRpcResponse
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRPCWebSocketRejectsRevokedCredentials(t *testing.T) {
	for _, kind := range []string{"deleted_session", "expired_session", "rotated_api_key", "rotated_agent_token"} {
		t.Run(kind, func(t *testing.T) {
			headers, query := http.Header{}, ""
			var revoke func()
			switch kind {
			case "deleted_session", "expired_session":
				_, session := revocationTestSession(t)
				headers.Set("Cookie", "session_token="+session)
				revoke = func() {
					if kind == "deleted_session" {
						if err := accounts.DeleteSession(session); err != nil {
							t.Fatal(err)
						}
					} else if err := dbcore.GetDBInstance().Model(&models.Session{}).Where("session = ?", session).Update("expires", time.Now().Add(-time.Minute)).Error; err != nil {
						t.Fatal(err)
					}
				}
			case "rotated_api_key":
				original, _ := config.GetAs[string](config.ApiKeyKey, "")
				t.Cleanup(func() { config.Set(config.ApiKeyKey, original) })
				if err := config.Set(config.ApiKeyKey, "test-original-api-key"); err != nil {
					t.Fatal(err)
				}
				headers.Set("Authorization", "Bearer test-original-api-key")
				// A valid cookie must not become a fallback after the API key is revoked.
				_, session := revocationTestSession(t)
				headers.Set("Cookie", "session_token="+session)
				revoke = func() {
					if err := config.Set(config.ApiKeyKey, "test-replacement-api-key"); err != nil {
						t.Fatal(err)
					}
				}
			case "rotated_agent_token":
				node := models.Client{UUID: t.Name(), Token: "test-original-agent-token", Name: "test"}
				if err := dbcore.GetDBInstance().Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { dbcore.GetDBInstance().Delete(&node) })
				query = "?token=" + node.Token
				revoke = func() {
					if err := dbcore.GetDBInstance().Model(&node).Update("token", "test-replacement-agent-token").Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			conn := dialRevocationTestRPC(t, headers, query)
			if response := revocationTestCall(t, conn, "rpc.ping", nil); response.Error != nil {
				t.Fatalf("valid credentials denied: %+v", response.Error)
			}
			revoke()
			response := revocationTestCall(t, conn, "rpc.ping", nil)
			if response.Error == nil || response.Error.Code != rpc.PermissionDenied {
				t.Fatalf("revoked credentials accepted: %+v", response)
			}
		})
	}
}

func TestRPCBatchRechecksSessionAfterRevocation(t *testing.T) {
	_, session := revocationTestSession(t)
	body := `[{"jsonrpc":"2.0","id":1,"method":"admin:deleteSession","params":{"session":"` + session + `"}},{"jsonrpc":"2.0","id":2,"method":"admin:getSessions"}]`
	r := gin.New()
	r.Use(api.IdentityMiddleware())
	r.POST("/api/rpc2", OnRpcRequest)
	q := httptest.NewRequest(http.MethodPost, "/api/rpc2", strings.NewReader(body))
	q.AddCookie(&http.Cookie{Name: "session_token", Value: session})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	var responses []rpc.JsonRpcResponse
	if err := json.Unmarshal(w.Body.Bytes(), &responses); err != nil {
		t.Fatal(err)
	}
	if len(responses) != 2 || responses[0].Error != nil || responses[1].Error == nil || responses[1].Error.Code != rpc.PermissionDenied {
		t.Fatalf("batch reused revoked identity: %+v", responses)
	}
}

func TestRPCWebSocketStillRequiresTwoFactorForEverySensitiveMessage(t *testing.T) {
	u, session := revocationTestSession(t)
	const secret = "JBSWY3DPEHPK3PXP"
	if err := accounts.Enable2Fa(u.UUID, secret); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("Cookie", "session_token="+session)
	conn := dialRevocationTestRPC(t, h, "")
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, params := range []map[string]any{nil, {"2fa_code": code}, nil} {
		response := revocationTestCall(t, conn, "admin:exec", params)
		// No command or target is provided, so a correct code must reach the
		// harmless parameter validation error without executing any command.
		want := rpc.PermissionDenied
		if params != nil {
			want = rpc.InvalidParams
		}
		if response.Error == nil || response.Error.Code != want {
			t.Fatalf("2FA gate result=%+v want code=%d", response.Error, want)
		}
	}
}

func TestAgentWebSocketRejectsRotatedToken(t *testing.T) {
	node := models.Client{UUID: t.Name(), Token: "test-v2-original-token", Name: "test"}
	if err := dbcore.GetDBInstance().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbcore.GetDBInstance().Delete(&node) })
	r := gin.New()
	r.Use(api.IdentityMiddleware())
	r.GET("/api/clients/v2/rpc", api.RequireRole(api.RoleClient), clientapi.WebSocketV2RPC)
	s := httptest.NewServer(r)
	t.Cleanup(s.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/api/clients/v2/rpc?token="+node.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	call := func() v2.Response {
		t.Helper()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := conn.WriteJSON(v2.Request{JSONRPC: v2.Version, ID: 1, Method: "test:no-operation"}); err != nil {
			t.Fatal(err)
		}
		var response v2.Response
		if err := conn.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	// The nonexistent method is a harmless authenticated dispatch probe.
	if response := call(); response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("valid agent did not reach method dispatch: %+v", response.Error)
	}
	if err := dbcore.GetDBInstance().Model(&node).Update("token", "test-v2-replacement-token").Error; err != nil {
		t.Fatal(err)
	}
	if response := call(); response.Error == nil || response.Error.Code != -32001 {
		t.Fatalf("revoked agent reached method dispatch: %+v", response.Error)
	}
}
