package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/config"
	"github.com/komari-monitor/komari/internal/metricstore"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	agentruntime "github.com/komari-monitor/komari/web/agent"
	"github.com/komari-monitor/komari/web/connection"
)

func setupLegacyClientTest(t *testing.T) string {
	t.Helper()
	flags.DatabaseType = "sqlite"
	flags.DatabaseFile = "file:legacy_clients_test?mode=memory&cache=shared"
	db := dbcore.GetDBInstance()
	for key, value := range map[string]any{
		config.GeoIpEnabledKey:        false,
		config.NotificationEnabledKey: false,
		metricstore.MetricDBDriverKey: "sqlite",
		metricstore.MetricDBDSNKey:    filepath.ToSlash(filepath.Join(t.TempDir(), "metrics.db")),
	} {
		if err := config.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := metricstore.InitializeStore(); err != nil {
		t.Fatal(err)
	}
	uuid := t.Name()
	if err := db.Create(&models.Client{UUID: uuid, Token: "test-" + uuid, Name: uuid}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		postPresenceMu.Lock()
		if entry := postPresenceStates[uuid]; entry != nil {
			entry.timer.Stop()
			agentruntime.SetPresence(uuid, entry.connID, false)
			delete(postPresenceStates, uuid)
		}
		postPresenceMu.Unlock()
		agentruntime.DeleteConnectedClients(uuid)
		agentruntime.DeleteLatestReport(uuid)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := metricstore.CloseStoreContext(ctx); err != nil {
			t.Error(err)
		}
	})
	return uuid
}

func legacyTestRouter(uuid string) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if uuid != "" {
			c.Set("client_uuid", uuid)
		}
	})
	router.POST("/report", UploadReport)
	router.POST("/basic", UploadBasicInfo)
	return router
}

func TestLegacyHTTPBindsAuthenticatedIdentityAndKeepsV1Presence(t *testing.T) {
	uuid := setupLegacyClientTest(t)
	router := legacyTestRouter(uuid)
	basic := httptest.NewRecorder()
	router.ServeHTTP(basic, httptest.NewRequest(http.MethodPost, "/basic",
		strings.NewReader(`{"uuid":"another-client","version":"1.2.60","os":"linux"}`)))
	if basic.Code != http.StatusOK {
		t.Fatalf("basic info: %d %s", basic.Code, basic.Body.String())
	}
	var stored models.Client
	if err := dbcore.GetDBInstance().First(&stored, "uuid = ?", uuid).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Version != "1.2.60" {
		t.Fatalf("basic info did not reach the authenticated client: %#v", stored.Version)
	}

	report := httptest.NewRecorder()
	router.ServeHTTP(report, httptest.NewRequest(http.MethodPost, "/report",
		strings.NewReader(`{"uuid":"another-client","cpu":{"usage":23},"network":{"totalUp":100,"totalDown":200}}`)))
	if report.Code != http.StatusOK {
		t.Fatalf("report: %d %s", report.Code, report.Body.String())
	}
	latest := agentruntime.GetLatestReport()
	if latest[uuid] == nil || latest[uuid].CPU.Usage != 23 || latest["another-client"] != nil {
		t.Fatal("report identity was taken from the request body")
	}
	if agentruntime.IsV2Client(uuid) {
		t.Fatal("legacy HTTP presence was mislabeled as v2")
	}
	found := false
	for _, id := range agentruntime.GetAllOnlineUUIDs() {
		found = found || id == uuid
	}
	if !found {
		t.Fatal("legacy HTTP reporter was not marked online")
	}
}

func TestLegacyHTTPRejectsMissingIdentityAndActiveV2(t *testing.T) {
	unauthenticated := httptest.NewRecorder()
	legacyTestRouter("").ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, "/report",
		strings.NewReader(`{"uuid":"forged","cpu":{"usage":20}}`)))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth: %d", unauthenticated.Code)
	}

	uuid := t.Name()
	agentruntime.RegisterConnectedClient(uuid, &connection.SafeConn{ID: 31}, 2)
	t.Cleanup(func() { agentruntime.DeleteConnectedClients(uuid) })
	blocked := httptest.NewRecorder()
	legacyTestRouter(uuid).ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/report",
		strings.NewReader(`{"cpu":{"usage":20}}`)))
	if blocked.Code != http.StatusConflict || !agentruntime.IsV2Client(uuid) {
		t.Fatalf("v1 POST replaced active v2: status=%d", blocked.Code)
	}
}

func TestLegacyWebSocketReportsAndReceivesTerminalRequest(t *testing.T) {
	uuid := setupLegacyClientTest(t)
	done := make(chan struct{})
	router := legacyTestRouter(uuid)
	router.GET("/report", func(c *gin.Context) {
		defer close(done)
		WebSocketReport(c)
	})
	server := httptest.NewServer(router)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/report?token=test", nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ws.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("legacy WS handler did not stop")
		}
		server.Close()
	})
	if err := ws.WriteJSON(map[string]any{"type": "report", "uuid": "spoofed", "cpu": map[string]any{"usage": 34}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if report := agentruntime.GetLatestReport()[uuid]; report != nil && report.CPU.Usage == 34 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("legacy WebSocket report was not ingested")
		}
		time.Sleep(time.Millisecond)
	}
	if agentruntime.IsV2Client(uuid) {
		t.Fatal("legacy WebSocket was marked as v2")
	}
	if !agentruntime.DispatchV2Event(uuid, v2.MethodAgentTerminal, v2.TerminalRequestParams{RequestID: "terminal-test"}) {
		t.Fatal("terminal dispatch to legacy agent failed")
	}
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event map[string]any
	if err := ws.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["message"] != "terminal" || event["request_id"] != "terminal-test" || event["jsonrpc"] != nil {
		t.Fatalf("legacy terminal envelope changed: %#v", event)
	}
}
