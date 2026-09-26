package metricstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/pkg/metric"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	sqlite3 "github.com/mattn/go-sqlite3"
)

type reportWriteFault struct {
	read  atomic.Bool
	flush atomic.Bool
}

func useReportWriteFailureStore(t *testing.T) (*metric.Store, *reportWriteFault) {
	t.Helper()
	fault := &reportWriteFault{}
	driver := &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		conn.RegisterAuthorizer(func(op int, table, _, _ string) int {
			if op == sqlite3.SQLITE_READ && table == "metric_definitions" && fault.read.Load() {
				return sqlite3.SQLITE_DENY
			}
			if op == sqlite3.SQLITE_INSERT && table == "metric_rollups" && fault.flush.Load() {
				return sqlite3.SQLITE_DENY
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	}}
	db := sql.OpenDB(&reportSQLiteConnector{driver: driver, dsn: fmt.Sprintf("file:report-write-fault-%d?mode=memory&cache=shared", time.Now().UnixNano())})
	s, err := metric.Open(context.Background(), metric.SQLite("", metric.WithDB(db), metric.WithMaxOpenConns(1)))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := createMetricDefinitions(context.Background(), s); err != nil {
		_ = s.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	storeMu.Lock()
	previous := store
	store = s
	storeMu.Unlock()
	clearReportTrafficStates()
	t.Cleanup(func() {
		fault.read.Store(false)
		fault.flush.Store(false)
		clearReportTrafficStates()
		storeMu.Lock()
		store = previous
		storeMu.Unlock()
		_ = s.Close()
		_ = db.Close()
	})
	return s, fault
}

func TestReportBatchRetryPreservesCountersAndTimestamps(t *testing.T) {
	s, fault := useReportWriteFailureStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	first := v2.Report{UUID: t.Name(), UpdatedAt: base, Network: v2.NetworkReport{TotalUp: 100, TotalDown: 200}}
	if _, err := WriteReport(ctx, first); err != nil {
		t.Fatal(err)
	}
	second, third := first, first
	second.UpdatedAt = base.Add(time.Second)
	second.Network.TotalUp, second.Network.TotalDown = 150, 260
	third.UpdatedAt = second.UpdatedAt // Exercise the monotonic adjustment, too.
	third.Network.TotalUp, third.Network.TotalDown = 170, 290
	pending := []v2.Report{second, third}
	backing := pending
	fault.read.Store(true)
	for range 3 {
		if err := writePendingReports(ctx, &pending); err == nil {
			t.Fatal("expected a pre-ingestion database failure")
		}
		if len(pending) != 2 || pending[0].UpdatedAt != second.UpdatedAt {
			t.Fatalf("failed batch changed: %#v", pending)
		}
	}
	fault.read.Store(false)
	if err := writePendingReports(ctx, &pending); err != nil {
		t.Fatal(err)
	}
	if pending != nil || backing[0].UUID != "" || backing[1].UUID != "" {
		t.Fatal("accepted reports retain their backing references")
	}
	assertMetricValues(t, s, MetricTrafficUp, first.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 50, 20})
	assertMetricValues(t, s, MetricTrafficDown, first.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 60, 30})
	points, err := s.Query(ctx, metric.Query{MetricName: MetricCPU, EntityID: first.UUID, Start: base, End: base.Add(time.Minute), Order: metric.OrderAsc})
	if err != nil || len(points) != 3 || !points[1].Timestamp.Equal(second.UpdatedAt) || !points[2].Timestamp.Equal(second.UpdatedAt.Add(time.Millisecond)) {
		t.Fatalf("retry changed timestamps: points=%#v, err=%v", points, err)
	}
}

func TestAcceptedFlushFailureDoesNotReplayExpiredReportOrPing(t *testing.T) {
	s, fault := useReportWriteFailureStore(t)
	ctx := context.Background()
	// These points are already outside the raw deduplication window. Replaying
	// them would double the retained rollup even with identical timestamps.
	base := time.Now().UTC().Truncate(time.Minute).Add(-11 * time.Minute)
	first := v2.Report{UUID: t.Name(), UpdatedAt: base, Network: v2.NetworkReport{TotalUp: 100, TotalDown: 200}}
	if _, err := WriteReport(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.UpdatedAt = base.Add(time.Second)
	second.Network.TotalUp, second.Network.TotalDown = 150, 260
	pending := []v2.Report{second}
	pings := []models.PingRecord{{Client: first.UUID, TaskId: 7, Value: 24, Time: base}}
	fault.flush.Store(true)
	for _, flush := range []func() error{
		func() error { return writePendingReports(ctx, &pending) },
		func() error { return writePendingPingRecords(ctx, &pings) },
	} {
		var accepted *metric.WriteAcceptedError
		if err := flush(); !errors.As(err, &accepted) {
			t.Fatalf("error = %v, want accepted flush failure", err)
		}
	}
	if pending != nil || pings != nil {
		t.Fatal("already accepted input was retained for replay")
	}
	if err := writePendingReports(ctx, &pending); err != nil {
		t.Fatal(err)
	}
	if err := writePendingPingRecords(ctx, &pings); err != nil {
		t.Fatal(err)
	}
	fault.flush.Store(false)
	if _, err := s.Flush(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	third := second
	third.UpdatedAt = base.Add(2 * time.Second)
	third.Network.TotalUp, third.Network.TotalDown = 170, 290
	if _, err := WriteReport(ctx, third); err != nil {
		t.Fatal(err)
	}
	assertMetricAggregate(t, s, MetricTrafficUp, first.UUID, base, base.Add(time.Minute), metric.AggSum, 70, 3)
	assertMetricAggregate(t, s, MetricTrafficDown, first.UUID, base, base.Add(time.Minute), metric.AggSum, 90, 3)
	assertMetricAggregate(t, s, MetricPingLatency, first.UUID, base, base.Add(time.Minute), metric.AggSum, 24, 1)
}

func TestFailedBatcherBoundsPendingAndDrainsEverythingOnStop(t *testing.T) {
	s, fault := useReportWriteFailureStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	StartReportBatcher()
	t.Cleanup(func() {
		fault.read.Store(false)
		if err := StopReportBatcher(context.Background()); err != nil {
			t.Error(err)
		}
	})
	base := time.Now().UTC().Truncate(time.Minute)
	fault.read.Store(true)
	for round := range 2 {
		for i := range reportBatchQueueSize {
			index := round*reportBatchQueueSize + i
			report := v2.Report{UUID: t.Name(), UpdatedAt: base.Add(time.Duration(index) * time.Millisecond), CPU: v2.CPUReport{Usage: 10}}
			if _, err := WriteReport(ctx, report); err != nil {
				t.Fatalf("queue report %d: %v", index, err)
			}
			if err := WritePingRecord(ctx, models.PingRecord{Client: report.UUID, TaskId: 7, Value: 24, Time: report.UpdatedAt}); err != nil {
				t.Fatalf("queue ping %d: %v", index, err)
			}
		}
		if err := FlushReportBatch(ctx); err == nil {
			t.Fatal("expected flush failure")
		}
	}
	for range 3 {
		if err := FlushReportBatch(ctx); err == nil {
			t.Fatal("expected repeated flush failure")
		}
	}
	if _, err := WriteReport(ctx, v2.Report{UUID: t.Name(), UpdatedAt: base}); !errors.Is(err, ErrReportBatchQueueFull) {
		t.Fatalf("report backpressure = %v", err)
	}
	if err := WritePingRecord(ctx, models.PingRecord{}); !errors.Is(err, ErrPingBatchQueueFull) {
		t.Fatalf("ping backpressure = %v", err)
	}
	if err := StopReportBatcher(ctx); err == nil {
		t.Fatal("shutdown hid the flush failure")
	}
	if _, err := WriteReport(ctx, v2.Report{UUID: t.Name(), UpdatedAt: base}); !errors.Is(err, ErrReportBatchStopped) {
		t.Fatalf("failed shutdown reopened intake: %v", err)
	}
	fault.read.Store(false)
	if err := StopReportBatcher(ctx); err != nil {
		t.Fatal(err)
	}
	assertMetricAggregate(t, s, MetricCPU, t.Name(), base, base.Add(time.Minute), metric.AggAvg, 10, 2*reportBatchQueueSize)
	assertMetricAggregate(t, s, MetricPingLatency, t.Name(), base, base.Add(time.Minute), metric.AggAvg, 24, 2*reportBatchQueueSize)
}

func TestConcurrentDirectReportsKeepEverySample(t *testing.T) {
	s := useReportTestStore(t, nil)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	const count = 32
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			if _, err := WriteReport(ctx, v2.Report{UUID: t.Name(), UpdatedAt: base, CPU: v2.CPUReport{Usage: 10}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	assertMetricAggregate(t, s, MetricCPU, t.Name(), base, base.Add(time.Minute), metric.AggAvg, 10, count)
}

func TestAcceptedFailureConsumesOnlyOneBoundedBatch(t *testing.T) {
	_, fault := useReportWriteFailureStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Minute).Add(-11 * time.Minute)
	var reports []v2.Report
	var pings []models.PingRecord
	for i := range reportBatchQueueSize + 1 {
		at := base.Add(time.Duration(i) * time.Millisecond)
		reports = append(reports, v2.Report{UUID: t.Name(), UpdatedAt: at})
		pings = append(pings, models.PingRecord{Client: t.Name(), Time: at, TaskId: 7})
	}
	reportBacking, pingBacking := reports, pings
	fault.flush.Store(true)
	for _, flush := range []func() error{
		func() error { return writePendingReports(ctx, &reports) },
		func() error { return writePendingPingRecords(ctx, &pings) },
	} {
		var accepted *metric.WriteAcceptedError
		if err := flush(); !errors.As(err, &accepted) {
			t.Fatalf("error = %v, want accepted flush failure", err)
		}
	}
	if len(reports) != 1 || len(pings) != 1 {
		t.Fatalf("remaining input = %d reports, %d pings, want one each", len(reports), len(pings))
	}
	for i := range reportBatchQueueSize {
		if reportBacking[i].UUID != "" || pingBacking[i].Client != "" {
			t.Fatal("accepted batch retained input references")
		}
	}
	if reports[0].UUID != t.Name() || pings[0].Client != t.Name() {
		t.Fatal("unaccepted input was cleared")
	}
}

func TestDirectWritesReturnAcceptedSamplesOnFlushFailure(t *testing.T) {
	s, fault := useReportWriteFailureStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Minute).Add(-11 * time.Minute)
	fault.flush.Store(true)
	report := v2.Report{UUID: t.Name(), UpdatedAt: base, CPU: v2.CPUReport{Usage: 10}}
	saved, err := WriteReport(ctx, report)
	if err != nil || saved.UUID != report.UUID {
		t.Fatalf("accepted direct report = %#v, err=%v", saved, err)
	}
	if err := WritePingRecord(ctx, models.PingRecord{Client: report.UUID, TaskId: 7, Value: 24, Time: base}); err != nil {
		t.Fatalf("accepted direct ping failed: %v", err)
	}
	fault.flush.Store(false)
	if _, err := s.Flush(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	assertMetricAggregate(t, s, MetricCPU, report.UUID, base, base.Add(time.Minute), metric.AggAvg, 10, 1)
	assertMetricAggregate(t, s, MetricPingLatency, report.UUID, base, base.Add(time.Minute), metric.AggAvg, 24, 1)
}

func TestFlushWaitingForReplyReturnsWhenWorkerStops(t *testing.T) {
	worker := &reportBatchWorker{requests: make(chan reportBatchRequest), done: make(chan struct{})}
	reportBatcherMu.Lock()
	reportBatcher = worker
	reportBatcherMu.Unlock()
	t.Cleanup(func() {
		reportBatcherMu.Lock()
		if reportBatcher == worker {
			reportBatcher = nil
		}
		reportBatcherMu.Unlock()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- FlushReportBatch(ctx) }()
	// Once the request is sent, stopping the worker must wake the reply wait
	// even if this request was queued behind the successful stop request.
	<-worker.requests
	close(worker.done)
	if err := <-result; !errors.Is(err, ErrReportBatchStopped) {
		t.Fatalf("flush after worker exit = %v", err)
	}
}
