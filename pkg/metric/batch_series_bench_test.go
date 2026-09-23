package metric

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkHistoryAccumulator(b *testing.B) {
	metas := make([]*seriesReadMeta, 128)
	for i := range metas {
		metas[i] = &seriesReadMeta{entityID: fmt.Sprintf("node-%d", i/4), tagsHash: fmt.Sprint(i % 4), tags: map[string]string{"task_id": fmt.Sprint(i % 4)}}
	}
	b.ReportAllocs()
	for b.Loop() {
		a := &metricSeriesAccumulator{spec: BatchSeriesSpec{MetricName: "ping.latency_ms", Aggregations: []Aggregation{AggAvg}, Interval: time.Minute, PreserveSeries: true}, compression: 100, needSummary: true, groups: make(map[rollupKey]*rollupAggregateState)}
		for bucket := 0; bucket < 120; bucket++ {
			for _, meta := range metas {
				a.consume(meta, int64(bucket)*60000, 5, 150, 5000, 10, 40, 10, 0, 40, 0, nil)
			}
		}
		result, err := a.points(OrderAsc)
		if err != nil || len(result[AggAvg]) != 128*120 {
			b.Fatalf("invalid history: %v", err)
		}
	}
}
