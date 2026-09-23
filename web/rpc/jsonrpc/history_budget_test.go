package jsonrpc

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestHistoryBudgetWaitCanBeCanceled(t *testing.T) {
	releases := make([]func(), 0, cap(historyQuerySlots))
	for range cap(historyQuerySlots) {
		_, release, err := acquireHistoryQuery(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := acquireHistoryQuery(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued query cancellation: %v", err)
	}
}

func TestMetricPointLimitAndIntervalOverflow(t *testing.T) {
	for _, params := range []publicMetricQueryParams{
		{MaxPoints: maxPublicMetricPoints + 1},
		{MaxPointsByMetric: map[string]int{"cpu.usage": math.MaxInt}},
		{MaxPointsByMetric: map[string]int{"cpu.usage": -1}},
	} {
		if _, err := resolveMetricMaxPoints("cpu.usage", params); err == nil {
			t.Fatal("invalid point limit accepted")
		}
	}
	if points, err := resolveMetricMaxPoints("cpu.usage", publicMetricQueryParams{}); err != nil || points != defaultMetricQueryPoints {
		t.Fatal("default point count changed")
	}
	if interval := metricDownsampleInterval(time.Duration(math.MaxInt64), 10000); interval <= time.Second {
		t.Fatalf("large interval overflowed: %v", interval)
	}
}
