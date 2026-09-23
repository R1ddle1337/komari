package jsonrpc

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
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
	encoded, err := json.Marshal([]publicMetricSeries{series})
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
}
