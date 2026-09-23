package agent

import (
	"sort"
	"sync"
	"time"

	v2 "github.com/komari-monitor/komari/protocol/v2"
	"github.com/komari-monitor/komari/web/connection"
)

type clientPresence struct {
	id     int64
	expire time.Time
}

var (
	connectedClients = make(map[string]*connection.SafeConn)
	latestReport     = make(map[string]*v2.Report)
	recentReports    = make(map[string][]v2.Report)
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

// RegisterConnectedClient atomically replaces a connection and returns its predecessor.
func RegisterConnectedClient(uuid string, conn *connection.SafeConn) *connection.SafeConn {
	mu.Lock()
	defer mu.Unlock()
	previous := connectedClients[uuid]
	connectedClients[uuid] = conn
	return previous
}

func GetConnectedClient(uuid string) *connection.SafeConn {
	mu.RLock()
	defer mu.RUnlock()
	return connectedClients[uuid]
}

func IsCurrentClientConnection(uuid string, conn *connection.SafeConn) bool {
	mu.RLock()
	defer mu.RUnlock()
	return connectedClients[uuid] == conn
}

func IsAgentOnline(uuid string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return connectedClients[uuid] != nil || presenceOnly[uuid].expire.After(time.Now())
}

func DeleteClientConditionally(uuid string, connToRemove *connection.SafeConn) {
	mu.Lock()
	defer mu.Unlock()

	// 检查当前 map 里的 conn 是否就是要删除的这一个
	if currentConn, exists := connectedClients[uuid]; exists && currentConn == connToRemove {
		delete(connectedClients, uuid)

	}
}
func DeleteConnectedClients(uuid string) {
	mu.Lock()
	defer mu.Unlock()
	// 只从 map 中删除，不再负责关闭连接
	delete(connectedClients, uuid)
}

// KeepAlivePresence refreshes the expiration for HTTP agents.
func KeepAlivePresence(uuid string, connectionID int64, ttl time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	presenceOnly[uuid] = clientPresence{id: connectionID, expire: time.Now().Add(ttl)}
}

// ClearPresence only removes the matching HTTP session.
func ClearPresence(uuid string, connectionID int64) {
	mu.Lock()
	defer mu.Unlock()
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
// window used by live status charts.
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
