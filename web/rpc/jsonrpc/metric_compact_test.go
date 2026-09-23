package jsonrpc

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/komari-monitor/komari/internal/metricstore"
	"github.com/komari-monitor/komari/pkg/metric"
)

func compactFixture() publicMetricSeries {
	values := []float64{0, -1, 0.00000001, 1.2345678901234567, 1e20}
	series := publicMetricSeries{MetricKey: "ping.latency_ms", EntityID: "node", Tags: map[string]string{"task_id": "7"}}
	for i := 0; i < 120; i++ {
		point := publicMetricPoint{Time: time.UnixMilli(1700000000000 + int64(i)*1250), Value: &values[i%len(values)], Count: i + 1, Tags: series.Tags}
		if i%7 == 0 {
			point.Value = nil
		}
		if i%13 == 0 {
			point.Labels = map[string]string{"device": "GPU <0> \"test\""}
		}
		series.Points = append(series.Points, point)
	}
	series.Count = len(series.Points)
	return series
}

func TestCompactMetricsPreserveSamplesAndMetadata(t *testing.T) {
	series := compactFixture()
	encoded, err := json.Marshal(compactMetricSeries([]publicMetricSeries{series}))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		MetricKey   string              `json:"metric_key"`
		EntityID    string              `json:"entity_id"`
		PointFormat string              `json:"point_format"`
		Tags        map[string]string   `json:"tags"`
		Count       int                 `json:"count"`
		Points      [][]json.RawMessage `json:"points"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded[0]
	if got.MetricKey != series.MetricKey || got.EntityID != series.EntityID || got.PointFormat != "points_v1" || !reflect.DeepEqual(got.Tags, series.Tags) || got.Count != len(series.Points) || len(got.Points) != len(series.Points) {
		t.Fatalf("metadata or sample count changed: %+v", got)
	}
	for i, tuple := range got.Points {
		var stamp int64
		var value *float64
		var count int
		if err := json.Unmarshal(tuple[0], &stamp); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(tuple[1], &value); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(tuple[2], &count); err != nil {
			t.Fatal(err)
		}
		point := series.Points[i]
		if stamp != point.Time.UnixMilli() || !reflect.DeepEqual(value, point.Value) || count != point.Count {
			t.Fatalf("sample %d changed", i)
		}
		if len(point.Labels) > 0 {
			var labels map[string]string
			if len(tuple) != 4 {
				t.Fatalf("sample %d lost labels", i)
			}
			if err := json.Unmarshal(tuple[3], &labels); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(labels, point.Labels) {
				t.Fatalf("sample %d labels changed", i)
			}
		} else if len(tuple) != 3 {
			t.Fatalf("unexpected tuple length %d", len(tuple))
		}
	}
	legacy, err := json.Marshal([]publicMetricSeries{series})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded)*100/len(legacy) > 65 {
		t.Fatalf("compact payload unexpectedly large: %d vs %d", len(encoded), len(legacy))
	}
	var ordinary []map[string]json.RawMessage
	if err := json.Unmarshal(legacy, &ordinary); err != nil {
		t.Fatal(err)
	}
	if _, ok := ordinary[0]["point_format"]; ok {
		t.Fatal("default format changed")
	}
}

func TestWeightedPingP95MatchesDashboard(t *testing.T) {
	points := []metric.AggregatePoint{{Value: 1000, Count: 5}, {Value: 12, Count: 95}, {Value: -1, Count: 500}, {Value: math.NaN(), Count: 20}, {Value: math.Inf(1), Count: 20}, {Value: 999, Count: 0}}
	if got := weightedAggregatePercentile(points, .95); got == nil || *got != 12 {
		t.Fatalf("p95 = %v", got)
	}
	points[0].Count = 6
	if got := weightedAggregatePercentile(points, .95); got == nil || *got != 1000 {
		t.Fatalf("weighted tail = %v", got)
	}
	if got := weightedAggregatePercentile(points[2:], .95); got != nil {
		t.Fatalf("invalid-only percentile = %v", got)
	}
}

func TestPingStatsReuseBucketP95(t *testing.T) {
	ctx := context.Background()
	store, err := metric.Open(ctx, metric.SQLite(filepath.Join(t.TempDir(), "metrics.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{metricstore.MetricPingLatency, metricstore.MetricPingLoss} {
		if err := store.CreateMetric(ctx, metric.Definition{Name: name, RetentionDays: 1}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Truncate(time.Minute).Add(-4 * time.Minute)
	points := make([]metric.Point, 0, 200)
	for i := 0; i < 100; i++ {
		stamp, value := base.Add(time.Duration(i)*time.Millisecond), 12.0
		if i >= 95 {
			stamp = stamp.Add(time.Minute)
			value = 1000
		}
		for _, sample := range []struct {
			name  string
			value float64
		}{{metricstore.MetricPingLatency, value}, {metricstore.MetricPingLoss, 0}} {
			points = append(points, metric.Point{MetricName: sample.name, EntityID: "node", Timestamp: stamp, Value: sample.value, Tags: map[string]string{"task_id": "7"}})
		}
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	groups, err := loadPublicPingMetricAggregateGroups(ctx, store, []string{"node"}, base, base.Add(2*time.Minute), time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stats := publicPingStatsFromAggregateGroups("node", groups["node"], nil, nil)
	if len(stats) != 1 || stats[0].P95 == nil || *stats[0].P95 != 12 || stats[0].Total != 100 || stats[0].Valid != 100 || stats[0].Loss != 0 {
		t.Fatalf("stats did not preserve weighted bucket P95: %+v", stats)
	}
}

func BenchmarkMetricResponseEncoding(b *testing.B) {
	series := compactFixture()
	for _, tc := range []struct {
		name  string
		value any
	}{{"default", []publicMetricSeries{series}}, {"compact", compactMetricSeries([]publicMetricSeries{series})}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := json.Marshal(tc.value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
