package agent

import (
	"time"

	v2 "github.com/komari-monitor/komari/protocol/v2"
	"github.com/komari-monitor/komari/web/connection"
)

// RegisterConnectedClient 原子切换连接与协议，禁止迟到的 v1 连接抢占活跃 v2。
func RegisterConnectedClient(uuid string, conn *connection.SafeConn, version int) (*connection.SafeConn, bool) {
	mu.Lock()
	defer mu.Unlock()
	if version < 2 && hasActiveV2ClientLocked(uuid) {
		return nil, false
	}
	previous := connectedClients[uuid]
	connectedClients[uuid] = conn
	connectionProtocols[uuid] = version
	if version >= 2 {
		v2Clients[uuid] = struct{}{}
	} else {
		delete(v2Clients, uuid)
	}
	return previous, true
}

func hasActiveV2ClientLocked(uuid string) bool {
	if connectedClients[uuid] != nil && connectionProtocols[uuid] >= 2 {
		return true
	}
	presence, ok := presenceOnly[uuid]
	return ok && presence.protocolVersion >= 2 && presence.expire.After(time.Now())
}

func HasActiveV2Client(uuid string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return hasActiveV2ClientLocked(uuid)
}

func IsCurrentClientConnection(uuid string, conn *connection.SafeConn) bool {
	mu.RLock()
	defer mu.RUnlock()
	return connectedClients[uuid] == conn
}

// RecordLegacyReport 拒绝旧协议的迟到数据覆盖已经接管的 v2 运行时缓存。
func RecordLegacyReport(report v2.Report) bool {
	mu.Lock()
	defer mu.Unlock()
	if hasActiveV2ClientLocked(report.UUID) {
		return false
	}
	delete(v2Clients, report.UUID)
	recordReportLocked(report)
	return true
}

// KeepAliveLegacyPresence 仅刷新 v1 状态，不把旧 HTTP 上报标成 v2。
func KeepAliveLegacyPresence(uuid string, connectionID int64, ttl time.Duration) bool {
	mu.Lock()
	defer mu.Unlock()
	if hasActiveV2ClientLocked(uuid) {
		return false
	}
	delete(v2Clients, uuid)
	presenceOnly[uuid] = clientPresence{id: connectionID, expire: time.Now().Add(ttl), protocolVersion: 1}
	return true
}

// agentEventPayload 仅为旧协议已有的命令、Ping、终端生成兼容报文。
// 文件流等新能力仍要求 v2，不能将 JSON-RPC 误发给旧 Agent。
func agentEventPayload(protocolVersion int, method string, params any) (any, bool) {
	if protocolVersion >= 2 {
		return v2.Request{JSONRPC: v2.Version, Method: method, Params: params}, true
	}
	switch method {
	case v2.MethodAgentExec:
		var p v2.ExecParams
		if bindV2EventParams(params, &p) != nil {
			return nil, false
		}
		return map[string]any{"message": "exec", "command": p.Command, "task_id": p.TaskID}, true
	case v2.MethodAgentPing:
		var p v2.PingParams
		if bindV2EventParams(params, &p) != nil {
			return nil, false
		}
		return map[string]any{"message": "ping", "ping_task_id": p.TaskID, "ping_type": p.Type, "ping_target": p.Target}, true
	case v2.MethodAgentTerminal:
		var p v2.TerminalRequestParams
		if bindV2EventParams(params, &p) != nil {
			return nil, false
		}
		return map[string]any{"message": "terminal", "request_id": p.RequestID}, true
	default:
		return nil, false
	}
}
