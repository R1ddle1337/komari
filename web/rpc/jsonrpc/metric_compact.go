package jsonrpc

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// points_v1 is the sole metric wire format: [unix_ms, value/null, count, labels?].
// Series tags are emitted once, while dynamic labels remain on each sample.
type compactMetricPoints []publicMetricPoint

func (series publicMetricSeries) MarshalJSON() ([]byte, error) {
	type wireSeries publicMetricSeries
	return json.Marshal(struct {
		wireSeries
		PointFormat string `json:"point_format"`
	}{wireSeries: wireSeries(series), PointFormat: "points_v1"})
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
