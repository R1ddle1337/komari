package jsonrpc

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/komari-monitor/komari/pkg/metric"
)

// Compact responses retain every sample, millisecond timestamp, null, weight
// and dynamic label. Tags are identical within a split series and are emitted
// once on the series. The default response remains unchanged for older clients.
// points_v1 tuples: [unix_milliseconds, value_or_null, count, optional_labels].
type compactPublicMetricSeries struct {
	publicMetricSeries
	PointFormat string              `json:"point_format"`
	Points      compactMetricPoints `json:"points"`
}

type compactMetricPoints []publicMetricPoint

func compactMetricSeries(series []publicMetricSeries) []compactPublicMetricSeries {
	out := make([]compactPublicMetricSeries, len(series))
	for i, item := range series {
		out[i] = compactPublicMetricSeries{publicMetricSeries: item, PointFormat: "points_v1", Points: compactMetricPoints(item.Points)}
	}
	return out
}

func (points compactMetricPoints) MarshalJSON() ([]byte, error) {
	buffer := make([]byte, 0, len(points)*40+2)
	buffer = append(buffer, '[')
	for i, point := range points {
		if i > 0 {
			buffer = append(buffer, ',')
		}
		buffer = append(buffer, '[')
		buffer = strconv.AppendInt(buffer, point.Time.UnixMilli(), 10)
		buffer = append(buffer, ',')
		if point.Value == nil {
			buffer = append(buffer, "null"...)
		} else {
			if math.IsInf(*point.Value, 0) || math.IsNaN(*point.Value) {
				return nil, fmt.Errorf("invalid metric value")
			}
			buffer = strconv.AppendFloat(buffer, *point.Value, 'g', -1, 64)
		}
		buffer = append(buffer, ',')
		buffer = strconv.AppendInt(buffer, int64(point.Count), 10)
		if len(point.Labels) > 0 {
			labels, err := json.Marshal(point.Labels)
			if err != nil {
				return nil, err
			}
			buffer = append(buffer, ',')
			buffer = append(buffer, labels...)
		}
		buffer = append(buffer, ']')
	}
	return append(buffer, ']'), nil
}

// Match the dashboard's existing weighted P95 of bucket P95s. Compute it from
// the already-loaded stats so the browser need not fetch the same history a
// second time. This is deliberately not described as an exact raw percentile.
func weightedAggregatePercentile(points []metric.AggregatePoint, quantile float64) *float64 {
	valid := make([]metric.AggregatePoint, 0, len(points))
	total := 0
	for _, point := range points {
		if point.Count > 0 && point.Value >= 0 && !math.IsNaN(point.Value) && !math.IsInf(point.Value, 0) {
			valid = append(valid, point)
			total += point.Count
		}
	}
	if total == 0 {
		return nil
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Value < valid[j].Value })
	count := 0
	for _, point := range valid {
		count += point.Count
		if float64(count) >= float64(total)*quantile {
			value := point.Value
			return &value
		}
	}
	value := valid[len(valid)-1].Value
	return &value
}
