package jsonrpc

import (
	"context"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/metricstore"
	"github.com/komari-monitor/komari/pkg/metric"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestPingStatisticsMergeFullWindowDistributions(t *testing.T) {
	ctx := context.Background()
	store, err := metric.Open(ctx, metric.SQLite(filepath.Join(t.TempDir(), "metric.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{metricstore.MetricPingLatency, metricstore.MetricPingLoss} {
		if err := store.CreateMetric(ctx, metric.Definition{Name: name, RetentionDays: 1}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Truncate(time.Minute).Add(-5 * time.Minute)
	points := []metric.Point{}
	add := func(entity, task string, stamp time.Time, value float64) {
		loss := 0.0
		if value < 0 {
			loss = 1
		}
		for _, sample := range []struct {
			name  string
			value float64
		}{{metricstore.MetricPingLatency, value}, {metricstore.MetricPingLoss, loss}} {
			points = append(points, metric.Point{MetricName: sample.name, EntityID: entity, Tags: map[string]string{"task_id": task}, Timestamp: stamp, Value: sample.value})
		}
	}
	for i := 0; i < 100; i++ {
		value, stamp := 10.0, base.Add(time.Duration(i)*time.Millisecond)
		if i >= 90 {
			value = 1000
			stamp = stamp.Add(time.Minute)
		}
		add("node-a", "7", stamp, value)
		add("node-b", "7", stamp, 50)
		if i < 90 {
			value = -1
		} else {
			value = 20
		}
		add("node-a", "8", stamp, value)
		add("node-a", "9", stamp, -1)
	}
	// Last sample failed; latest must remain unknown, not a bucket average.
	add("node-a", "8", base.Add(2*time.Minute), -1)
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T) {
		stats, err := loadPublicPingStats(ctx, store, []string{"node-a", "node-b"}, base, base.Add(3*time.Minute), time.Minute, time.Now(), map[string]models.PingTask{"7": {Name: "fixture", Type: "tcp", Interval: 5}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(stats) != 4 {
			t.Fatalf("series boundaries lost: %#v", stats)
		}
		for _, s := range stats {
			switch {
			case s.EntityID == "node-b":
				if s.Avg == nil || *s.Avg != 50 || s.Total != 100 || s.StdDev == nil || *s.StdDev != 0 {
					t.Fatalf("entity mixing: %#v", s)
				}
			case s.TaskID == "7":
				sd := math.Sqrt(.9*math.Pow(10-109, 2) + .1*math.Pow(1000-109, 2))
				if s.Name != "fixture" || s.Type != "tcp" || s.Interval != 5 || s.Total != 100 || s.Valid != 100 || s.Avg == nil || *s.Avg != 109 || s.StdDev == nil || math.Abs(*s.StdDev-sd) > 1e-6 {
					t.Fatalf("global moments wrong: %#v", s)
				}
				if s.P50 == nil || math.Abs(*s.P50-10) > 2 || s.P95 == nil || *s.P95 < 900 || s.P99 == nil || *s.P99 < 990 {
					t.Fatalf("global percentiles wrong: p50=%v p95=%v p99=%v", *s.P50, *s.P95, *s.P99)
				}
			case s.TaskID == "8":
				if s.Total != 101 || s.Valid != 10 || math.Abs(s.Loss-100*91.0/101) > 1e-6 || s.Avg == nil || *s.Avg != 20 || s.StdDev == nil || *s.StdDev != 0 || s.Min != nil || s.Latest != nil {
					t.Fatalf("loss correction wrong: %#v", s)
				}
				if s.P95 == nil || math.Abs(*s.P95-20) > 1 {
					t.Fatalf("conditional percentile wrong: %v", s.P95)
				}
			case s.TaskID == "9":
				if s.Loss != 100 || s.Valid != 0 || s.Avg != nil || s.P95 != nil || s.StdDev != nil {
					t.Fatalf("all-loss not null: %#v", s)
				}
			}
		}
		filtered, err := loadPublicPingStats(ctx, store, []string{"node-a"}, base, base.Add(3*time.Minute), time.Minute, time.Now(), nil, map[string]bool{"8": true})
		if err != nil || len(filtered) != 1 || filtered[0].TaskID != "8" {
			t.Fatalf("filter failed: %#v %v", filtered, err)
		}
	}
	t.Run("memory", check)
	if _, err := store.Flush(ctx, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	t.Run("persisted", check)
}

func TestPingStatisticsDoNotInventMissingLossHistory(t *testing.T) {
	stat := publicPingStatsFromDistribution(metric.Distribution{Count: 10, Min: -1, Sum: 15}, metric.Distribution{}, false)
	if !stat.LossApproximate || stat.Avg != nil || stat.P95 != nil || stat.Min != nil {
		t.Fatalf("missing loss history fabricated stats: %#v", stat)
	}
}
