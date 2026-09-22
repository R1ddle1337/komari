package client

import (
	"context"
	"errors"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/internal/metricstore"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
)

// ingest.go
// agent 上报数据的传输无关处理逻辑。v1 兼容入口与 v2 HTTP/WebSocket 入口
// 经过协议解析后，统一调用这里的函数落库并更新运行时状态。

// ingestReport 保存一次负载上报并刷新运行时状态。
// markPresence 为 true 时按 POST 上报会话刷新在线状态（WS 连接自行管理在线状态，应传 false）。
func ingestReport(uuid string, report v2.Report, markPresence bool) error {
	return ingestReportForProtocol(uuid, report, 2, markPresence)
}

var errLegacyProtocolSuperseded = errors.New("an active v2 connection already owns this client")

func ingestReportForProtocol(uuid string, report v2.Report, protocolVersion int, markPresence bool) error {
	if protocolVersion < 2 && agent_runtime.HasActiveV2Client(uuid) {
		return errLegacyProtocolSuperseded
	}
	report.UUID = uuid
	report.UpdatedAt = time.Now().UTC()
	if err := clients.ReportVerify(report); err != nil {
		return err
	}
	savedReport, err := metricstore.WriteReport(context.Background(), report)
	if err != nil {
		return err
	}
	// 先登记 HTTP 协议归属，再更新缓存，关闭 v2 POST 接管时的旧报告覆盖窗口。
	if markPresence && !refreshPostPresenceForProtocol(uuid, protocolVersion) {
		return errLegacyProtocolSuperseded
	}
	if protocolVersion >= 2 {
		agent_runtime.RecordReport(savedReport)
		agent_runtime.MarkV2Client(uuid)
	} else if !agent_runtime.RecordLegacyReport(savedReport) {
		// v2 可能在落库期间接管；已接收采样保留，但旧连接不能覆盖运行时状态。
		return errLegacyProtocolSuperseded
	}
	return nil
}

// ingestBasicInfo 保存客户端基础信息。fallbackIP 在上报未携带 IP 时用作兜底。
func ingestBasicInfo(uuid string, info map[string]interface{}, fallbackIP string) error {
	if info == nil {
		info = map[string]interface{}{}
	}
	return saveClientBasicInfo(info, uuid, fallbackIP)
}

// ingestPingResult 保存一条 ping 探测结果。
func ingestPingResult(uuid string, taskID uint, value int) error {
	return tasks.SavePingRecord(models.PingRecord{
		Client: uuid,
		TaskId: taskID,
		Value:  value,
		Time:   time.Now().UTC(),
	})
}
