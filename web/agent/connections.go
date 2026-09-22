package agent

import (
	"sort"
	"sync"
	"time"

	v2 "github.com/komari-monitor/komari/protocol/v2"
	"github.com/komari-monitor/komari/web/connection"
)

type clientPresence struct {
	id              int64
	expire          time.Time
	protocolVersion int
}

var (
	connectedClients    = make(map[string]*connection.SafeConn)
	connectionProtocols = make(map[string]int)
	v2Clients           = make(map[string]struct{})
	latestReport        = make(map[string]*v2.Report)
	recentReports       = make(map[string][]v2.Report)
	// presenceOnly stores online state for non-WebSocket agents.
	// value keeps connectionID and a soft expiration to avoid flicker
	presenceOnly = make(map[string]clientPresence)
	mu           = sync.RWMutex{}
)

const recentReportRetention = time.Minute

func GetConnectedClients() map[string]*connection.SafeConn {
	mu.RLock()
	defer mu.RUnlock()
	clientsCopy := make(map[string]*connection.SafeConn)
	for k, v := range connectedClients {
		clientsCopy[k] = v
	}
	return clientsCopy
}

func SetConnectedClients(uuid string, conn *connection.SafeConn) {
	mu.Lock()
	defer mu.Unlock()
	connectedClients[uuid] = conn
	connectionProtocols[uuid] = 2
}

// GetConnectedClient 同步读取连接与其协议，防止重连期间把新格式发给旧连接。
func GetConnectedClient(uuid string) (*connection.SafeConn, int) {
	mu.RLock()
	defer mu.RUnlock()
	return connectedClients[uuid], connectionProtocols[uuid]
}

func MarkV2Client(uuid string) {
	mu.Lock()
	defer mu.Unlock()
	v2Clients[uuid] = struct{}{}
}

func IsV2Client(uuid string) bool {
	mu.RLock()
	defer mu.RUnlock()
	if connectedClients[uuid] != nil {
		return connectionProtocols[uuid] >= 2
	}
	_, ok := v2Clients[uuid]
	return ok
}

func DeleteClientConditionally(uuid string, connToRemove *connection.SafeConn) {
	mu.Lock()
	defer mu.Unlock()

	// 检查当前 map 里的 conn 是否就是要删除的这一个
	if currentConn, exists := connectedClients[uuid]; exists && currentConn == connToRemove {
		delete(connectedClients, uuid)
		delete(connectionProtocols, uuid)
		if presence := presenceOnly[uuid]; presence.protocolVersion >= 2 && presence.expire.After(time.Now()) {
			v2Clients[uuid] = struct{}{}
		} else {
			delete(v2Clients, uuid)
		}
	}
}
func DeleteConnectedClients(uuid string) {
	mu.Lock()
	defer mu.Unlock()
	// 只从 map 中删除，不再负责关闭连接
	delete(connectedClients, uuid)
	delete(connectionProtocols, uuid)
	delete(v2Clients, uuid)
}

// SetPresence sets or clears presence for non-WebSocket agents.
// When present=false, it only clears if the connectionID matches current one.
// KeepAlivePresence sets presence with TTL for non-WebSocket agents.
func KeepAlivePresence(uuid string, connectionID int64, ttl time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	presenceOnly[uuid] = clientPresence{id: connectionID, expire: time.Now().Add(ttl), protocolVersion: 2}
}

var defaultPresenceTTL = 20 * time.Second

// SetPresence keeps compatibility with existing callers.
func SetPresence(uuid string, connectionID int64, present bool) {
	mu.Lock()
	defer mu.Unlock()
	if present {
		presenceOnly[uuid] = clientPresence{id: connectionID, expire: time.Now().Add(defaultPresenceTTL), protocolVersion: 2}
		return
	}
	if cur, ok := presenceOnly[uuid]; ok && cur.id == connectionID {
		delete(presenceOnly, uuid)
	}
}

// GetAllOnlineUUIDs returns a de-duplicated list of online UUIDs from both WebSocket and non-WebSocket agents.
func GetAllOnlineUUIDs() []string {
	mu.RLock()
	defer mu.RUnlock()
	set := make(map[string]struct{})
	for k := range connectedClients {
		set[k] = struct{}{}
	}
	now := time.Now()
	for k, v := range presenceOnly {
		if v.expire.After(now) {
			set[k] = struct{}{}
		}
	}
	res := make([]string, 0, len(set))
	for k := range set {
		res = append(res, k)
	}
	return res
}
func GetLatestReport() map[string]*v2.Report {
	mu.RLock()
	defer mu.RUnlock()
	reportCopy := make(map[string]*v2.Report)
	for k, v := range latestReport {
		if v == nil {
			continue
		}
		item := *v
		reportCopy[k] = &item
	}
	return reportCopy
}

// RecordReport updates the latest runtime state and keeps only the short raw
// window used by recent-status compatibility endpoints.
func RecordReport(report v2.Report) {
	mu.Lock()
	defer mu.Unlock()
	recordReportLocked(report)
}

func recordReportLocked(report v2.Report) {
	if report.UUID == "" {
		return
	}
	if report.UpdatedAt.IsZero() {
		report.UpdatedAt = time.Now().UTC()
	} else {
		report.UpdatedAt = report.UpdatedAt.UTC()
	}
	if latest := latestReport[report.UUID]; latest == nil || !report.UpdatedAt.Before(latest.UpdatedAt) {
		item := report
		latestReport[report.UUID] = &item
	}
	cutoff := time.Now().UTC().Add(-recentReportRetention)
	reports := reportsAfter(recentReports[report.UUID], cutoff)
	if report.UpdatedAt.Before(cutoff) {
		recentReports[report.UUID] = reports
		return
	}
	insertAt := sort.Search(len(reports), func(i int) bool {
		return reports[i].UpdatedAt.After(report.UpdatedAt)
	})
	reports = append(reports, v2.Report{})
	copy(reports[insertAt+1:], reports[insertAt:])
	reports[insertAt] = report
	recentReports[report.UUID] = reports
}

func GetRecentReports(uuid string) []v2.Report {
	mu.Lock()
	defer mu.Unlock()
	reports := reportsAfter(recentReports[uuid], time.Now().UTC().Add(-recentReportRetention))
	if len(reports) == 0 {
		delete(recentReports, uuid)
		return []v2.Report{}
	}
	recentReports[uuid] = reports
	return append([]v2.Report(nil), reports...)
}

func reportsAfter(reports []v2.Report, cutoff time.Time) []v2.Report {
	first := 0
	for first < len(reports) && reports[first].UpdatedAt.Before(cutoff) {
		first++
	}
	out := make([]v2.Report, len(reports)-first)
	copy(out, reports[first:])
	return out
}

func DeleteLatestReport(uuid string) {
	mu.Lock()
	defer mu.Unlock()
	delete(latestReport, uuid)
	delete(recentReports, uuid)
}
