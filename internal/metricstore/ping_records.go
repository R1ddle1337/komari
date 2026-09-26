package metricstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/pkg/metric"
	logger "github.com/komari-monitor/komari/utils/log"
)

// WritePingRecord 将 ping 记录写入 metric store
func WritePingRecord(ctx context.Context, rec models.PingRecord) error {
	s := GetStore()
	if s == nil {
		return fmt.Errorf("metric store not enabled")
	}

	reportBatcherMu.Lock()
	worker := reportBatcher
	reportBatcherMu.Unlock()
	if worker != nil {
		return worker.enqueuePing(ctx, rec)
	}
	err := writePingRecords(ctx, []models.PingRecord{rec})
	var accepted *metric.WriteAcceptedError
	if errors.As(err, &accepted) {
		logger.Errorf("metricstore", "ping accepted; rollup persistence will retry: %v", err)
		return nil
	}
	return err
}

func writePingRecords(ctx context.Context, records []models.PingRecord) error {
	if err := storeOperations.AcquireShared(ctx); err != nil {
		return fmt.Errorf("wait for metric store operation before writing pings: %w", err)
	}
	defer storeOperations.ReleaseShared()
	s := GetStore()
	if s == nil {
		return fmt.Errorf("metric store not enabled")
	}
	if len(records) == 0 {
		return nil
	}

	points := make([]metric.Point, 0, len(records)*2)
	for _, rec := range records {
		tags := map[string]string{"task_id": fmt.Sprintf("%d", rec.TaskId)}
		loss := 0.0
		if rec.Value < 0 {
			loss = 1
		}
		points = append(points,
			metric.Point{
				MetricName: MetricPingLatency,
				EntityID:   rec.Client,
				Timestamp:  rec.Time,
				Value:      float64(rec.Value),
				Tags:       tags,
			},
			metric.Point{
				MetricName: MetricPingLoss,
				EntityID:   rec.Client,
				Timestamp:  rec.Time,
				Value:      loss,
				Tags:       tags,
			},
		)
	}
	return s.WriteBatch(ctx, points)
}
