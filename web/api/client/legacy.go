package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	agentruntime "github.com/komari-monitor/komari/web/agent"
	"github.com/komari-monitor/komari/web/api"
)

const legacyReportLimit = 4 << 20

// legacyClientUUID 只使用已鉴权的上下文/令牌，绝不信任报文中的 UUID。
func legacyClientUUID(c *gin.Context) (string, bool) {
	uuid, ok := clientUUIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "Invalid token"})
		return "", false
	}
	if agentruntime.HasActiveV2Client(uuid) {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": errLegacyProtocolSuperseded.Error()})
		return "", false
	}
	return uuid, true
}

func UploadBasicInfo(c *gin.Context) {
	uuid, ok := legacyClientUUID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, legacyReportLimit)
	var info map[string]interface{}
	if err := c.ShouldBindJSON(&info); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Invalid data"})
		return
	}
	if err := ingestBasicInfo(uuid, info, c.ClientIP()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func UploadReport(c *gin.Context) {
	uuid, ok := legacyClientUUID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, legacyReportLimit)
	var report v2.Report
	if err := c.ShouldBindJSON(&report); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Invalid report"})
		return
	}
	if err := ingestReportForProtocol(uuid, report, 1, true); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errLegacyProtocolSuperseded) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"status": "error", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func WebSocketReport(c *gin.Context) {
	uuid, ok := legacyClientUUID(c)
	if !ok {
		return
	}
	if !api.IsWebSocketUpgrade(c) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Require WebSocket upgrade"})
		return
	}
	conn, err := api.UpgradeSafeConn(c)
	if err != nil {
		return
	}
	defer conn.Close()
	previous, accepted := agentruntime.RegisterConnectedClient(uuid, conn, 1)
	if !accepted {
		conn.WriteJSON(gin.H{"status": "error", "error": errLegacyProtocolSuperseded.Error()})
		return
	}
	if previous != nil {
		go previous.Close()
	}
	notifierOnline(uuid, conn.ID)
	defer func() {
		agentruntime.DeleteClientConditionally(uuid, conn)
		notifierOffline(uuid, conn.ID)
	}()
	conn.GetConn().SetReadLimit(legacyReportLimit)
	for {
		conn.SetReadDeadline(time.Now().Add(readWait))
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if !agentruntime.IsCurrentClientConnection(uuid, conn) {
			return
		}
		if err := processLegacyMessage(uuid, message); err != nil {
			if errors.Is(err, errLegacyProtocolSuperseded) {
				return
			}
			if conn.WriteJSON(gin.H{"status": "error", "error": err.Error()}) != nil {
				return
			}
		}
	}
}

// v1/v2 监控 report 的字段保持一致，只转换信封；沿用新版存储与校验。
func processLegacyMessage(uuid string, message []byte) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil {
		return err
	}
	switch envelope.Type {
	case "", "report":
		var report v2.Report
		if err := json.Unmarshal(message, &report); err != nil {
			return err
		}
		return ingestReportForProtocol(uuid, report, 1, false)
	case "ping_result":
		var result struct {
			TaskID uint `json:"task_id"`
			Value  int  `json:"value"`
		}
		if err := json.Unmarshal(message, &result); err != nil {
			return err
		}
		return ingestPingResult(uuid, result.TaskID, result.Value)
	default:
		return errors.New("unknown legacy message type")
	}
}
