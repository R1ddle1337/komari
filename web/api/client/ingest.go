package client

import (
	"context"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/internal/metricstore"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
)

// ingest.go
// v2 HTTP/WebSocket 上报共用的入库与运行时状态处理。

// ingestReport 保存一次负载上报并刷新运行时状态。
// markPresence 为 true 时按 POST 上报会话刷新在线状态（WS 连接自行管理在线状态，应传 false）。
func ingestReport(uuid string, report v2.Report, markPresence bool) error {
	report.UUID = uuid
	report.UpdatedAt = time.Now().UTC()
	if err := clients.ReportVerify(report); err != nil {
		return err
	}
	savedReport, err := metricstore.WriteReport(context.Background(), report)
	if err != nil {
		return err
	}
	if markPresence {
		refreshPostPresence(uuid)
	}
	agent_runtime.RecordReport(savedReport)
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
