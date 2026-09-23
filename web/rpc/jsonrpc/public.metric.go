package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/internal/metricstore"
	"github.com/komari-monitor/komari/pkg/metric"
	"github.com/komari-monitor/komari/pkg/rpc"
)

const defaultMetricQueryPoints = 500
const maxPublicMetricPoints = 10000
const maxPublicRawPoints = 100000
const maxPublicMetricBudget = 1000000
const maxPublicMetricKeys = 64

func init() {
	regPublic("listMetricDefinitions", publicListMetricDefinitions, "List public metric definitions")
	regPublic("queryMetrics", publicQueryMetrics, "Query metric points")
	regPublic("getPingMetricStats", publicGetPingMetricStats, "Get ping metric statistics")
}

type publicMetricQueryParams struct {
	MetricKey  string   `json:"metric_key"`
	MetricKeys []string `json:"metric_keys"`

	EntityID  string   `json:"entity_id"`
	EntityIDs []string `json:"entity_ids"`

	Start *time.Time `json:"start"`
	End   *time.Time `json:"end"`
	Hours float64    `json:"hours"`

	Tags map[string]string `json:"tags"`

	FillEmpty *bool `json:"fill_empty"`

	MaxPoints         int            `json:"max_points"`
	MaxPointsByMetric map[string]int `json:"max_points_by_metric"`

	Aggregation         string            `json:"aggregation"`
	AggregationByMetric map[string]string `json:"aggregation_by_metric"`
}

type publicMetricPoint struct {
	entityID string
	Time     time.Time         `json:"time"`
	Value    *float64          `json:"value"`
	Count    int               `json:"count,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

type publicMetricSeries struct {
	MetricKey           string              `json:"metric_key"`
	EntityID            string              `json:"entity_id"`
	Type                string              `json:"type,omitempty"`
	Unit                string              `json:"unit,omitempty"`
	RetentionDays       int                 `json:"retention_days,omitempty"`
	Tags                map[string]string   `json:"tags,omitempty"`
	Downsampled         bool                `json:"downsampled"`
	DownsampleAlgorithm string              `json:"downsample_algorithm,omitempty"`
	FillEmpty           bool                `json:"fill_empty,omitempty"`
	MaxPoints           int                 `json:"max_points,omitempty"`
	IntervalSeconds     float64             `json:"interval_seconds,omitempty"`
	Count               int                 `json:"count"`
	Points              compactMetricPoints `json:"points"`
}

type publicPingMetricStatsParams struct {
	EntityID  string   `json:"entity_id"`
	EntityIDs []string `json:"entity_ids"`

	TaskID  any   `json:"task_id"`
	TaskIDs []any `json:"task_ids"`

	Start *time.Time `json:"start"`
	End   *time.Time `json:"end"`
	Hours float64    `json:"hours"`
}

type publicPingMetricTaskStats struct {
	EntityID             string            `json:"entity_id"`
	TaskID               string            `json:"task_id"`
	Name                 string            `json:"name,omitempty"`
	Type                 string            `json:"type,omitempty"`
	Interval             int               `json:"interval,omitempty"`
	Tags                 map[string]string `json:"tags,omitempty"`
	Total                int               `json:"total"`
	Valid                int               `json:"valid"`
	Loss                 float64           `json:"loss"`
	LossApproximate      bool              `json:"loss_approximate,omitempty"`
	QuantilesApproximate bool              `json:"quantiles_approximate"`
	Min                  *float64          `json:"min,omitempty"`
	Max                  *float64          `json:"max,omitempty"`
	Avg                  *float64          `json:"avg,omitempty"`
	Latest               *float64          `json:"latest,omitempty"`
	P50                  *float64          `json:"p50,omitempty"`
	P95                  *float64          `json:"p95,omitempty"`
	P99                  *float64          `json:"p99,omitempty"`
	StdDev               *float64          `json:"stddev,omitempty"`
	P99P50Ratio          float64           `json:"p99_p50_ratio"`
}

type publicPingMetricStatsResponse struct {
	Start           time.Time                   `json:"start"`
	End             time.Time                   `json:"end"`
	IntervalSeconds float64                     `json:"interval_seconds,omitempty"`
	Stats           []publicPingMetricTaskStats `json:"stats"`
	Count           int                         `json:"count"`
}

func publicListMetricDefinitions(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	store := metricstore.GetStore()
	if store == nil {
		return nil, rpc.MakeError(rpc.InternalError, "metric store not initialized", nil)
	}
	defs, err := store.ListMetrics(ctx)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list metric definitions: "+err.Error(), nil)
	}
	out := make([]metricDefinitionResponse, 0, len(defs))
	for _, def := range defs {
		out = append(out, metricDefinitionResponse{
			Name:          def.Name,
			Description:   metricDescriptionValue(def.Description),
			Type:          string(def.Type),
			Unit:          def.Unit,
			RetentionDays: def.RetentionDays,
			Metadata:      def.Metadata,
			CreatedAt:     def.CreatedAt,
			UpdatedAt:     def.UpdatedAt,
		})
	}
	return out, nil
}

func publicQueryMetrics(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	ctx, release, err := acquireHistoryQuery(ctx)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "history query canceled or timed out", nil)
	}
	defer release()
	var params publicMetricQueryParams
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid request body: "+err.Error(), nil)
	}

	metricKeys := normalizeStringList(params.MetricKeys, []string{params.MetricKey})
	if len(metricKeys) == 0 || len(metricKeys) > maxPublicMetricKeys {
		return nil, rpc.MakeError(rpc.InvalidParams, "metric_keys must contain between 1 and 64 metrics", nil)
	}

	queryNow := time.Now().UTC()
	end := metricQueryTimeOrDefault(params.End, queryNow)
	startFallback := end.Add(-metricQueryHours(params.Hours))
	start := metricQueryTimeOrDefault(params.Start, startFallback)
	if !end.After(start) {
		return nil, rpc.MakeError(rpc.InvalidParams, "end must be after start", nil)
	}

	requestedEntityIDs := normalizeStringList(params.EntityIDs, []string{params.EntityID})
	entityIDs, rpcErr := publicMetricEntityIDs(ctx, requestedEntityIDs)
	if rpcErr != nil {
		return nil, rpcErr
	}

	store := metricstore.GetStore()
	if store == nil {
		return nil, rpc.MakeError(rpc.InternalError, "metric store not initialized", nil)
	}

	type metricLoadSpec struct {
		metricKey string
		algorithm metric.Aggregation
		maxPoints int
		interval  time.Duration
	}
	loadSpecs := make([]metricLoadSpec, 0, len(metricKeys))
	requestedPoints := 0
	for _, metricKey := range metricKeys {
		maxPoints, err := resolveMetricMaxPoints(metricKey, params)
		if err != nil {
			return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
		}
		requestedPoints += maxPoints
		loadSpecs = append(loadSpecs, metricLoadSpec{
			metricKey: metricKey,
			algorithm: resolveMetricAggregation(metricKey, params),
			maxPoints: maxPoints,
		})
	}

	if len(entityIDs) > 0 && requestedPoints > maxPublicMetricBudget/len(entityIDs) {
		return nil, rpc.MakeError(rpc.InvalidParams, "query exceeds 1000000 requested points; reduce entities, metrics, or max_points", nil)
	}

	metricFillEmpty := resolveMetricFillEmpty(params)
	useRaw := publicMetricUsesRawWindow(start, end, queryNow)
	definitions := make(map[string]metric.Definition, len(metricKeys))
	rawValues := make(map[string][]metric.Point)
	rollupValues := make(map[string]map[metric.Aggregation][]metric.AggregatePoint)
	if len(entityIDs) > 0 && useRaw {
		var err error
		definitions, err = store.GetMetrics(ctx, metricKeys)
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to query metric definitions: "+err.Error(), nil)
		}
		for _, spec := range loadSpecs {
			if _, ok := definitions[spec.metricKey]; !ok {
				return nil, rpc.MakeError(rpc.InvalidParams, "unknown metric key: "+spec.metricKey, nil)
			}
		}
		rawValues, err = store.QueryBatch(ctx, metric.BatchQuery{
			MaxPoints:   maxPublicRawPoints,
			MetricNames: metricKeys,
			EntityIDs:   entityIDs,
			Start:       start,
			End:         end,
			Tags:        params.Tags,
			Order:       metric.OrderAsc,
		})
		if errors.Is(err, metric.ErrQueryPointLimit) {
			useRaw = false
			rawValues = nil
		} else if err != nil {
			return nil, rpc.MakeError(rpc.InvalidParams, "Failed to query metrics: "+err.Error(), nil)
		}
	}
	if len(entityIDs) > 0 && !useRaw {
		batchSpecs := make([]metric.BatchSeriesSpec, 0, len(loadSpecs))
		for i := range loadSpecs {
			loadSpecs[i].interval = metricDownsampleInterval(end.Sub(start), loadSpecs[i].maxPoints)
			loadSpecs[i].interval = store.CompatibleSeriesInterval(start, queryNow, loadSpecs[i].interval)
			batchSpecs = append(batchSpecs, metric.BatchSeriesSpec{
				MetricName:     loadSpecs[i].metricKey,
				Aggregations:   []metric.Aggregation{loadSpecs[i].algorithm},
				Interval:       loadSpecs[i].interval,
				PreserveSeries: true,
			})
		}
		loaded, err := store.SeriesBatch(ctx, metric.BatchSeriesQuery{
			Specs:     batchSpecs,
			EntityIDs: entityIDs,
			Start:     start,
			End:       end,
			Tags:      params.Tags,
			Order:     metric.OrderAsc,
		}, queryNow)
		if err != nil {
			return nil, rpc.MakeError(rpc.InvalidParams, "Failed to query metrics: "+err.Error(), nil)
		}
		definitions = loaded.Definitions
		rollupValues = loaded.Values
		for _, spec := range loadSpecs {
			if _, ok := definitions[spec.metricKey]; !ok {
				return nil, rpc.MakeError(rpc.InvalidParams, "unknown metric key: "+spec.metricKey, nil)
			}
		}
	} else if len(entityIDs) == 0 {
		var err error
		definitions, err = store.GetMetrics(ctx, metricKeys)
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to query metric definitions: "+err.Error(), nil)
		}
		for _, spec := range loadSpecs {
			if _, ok := definitions[spec.metricKey]; !ok {
				return nil, rpc.MakeError(rpc.InvalidParams, "unknown metric key: "+spec.metricKey, nil)
			}
		}
	}

	series := make([]publicMetricSeries, 0, len(metricKeys)*maxInt(1, len(entityIDs)))
	for _, spec := range loadSpecs {
		def := definitions[spec.metricKey]
		item := publicMetricSeries{
			MetricKey:     spec.metricKey,
			Type:          string(def.Type),
			Unit:          def.Unit,
			RetentionDays: def.RetentionDays,
			Tags:          params.Tags,
			FillEmpty:     metricFillEmpty,
			MaxPoints:     spec.maxPoints,
			Downsampled:   !useRaw,
		}
		if useRaw {
			points := rawValues[spec.metricKey]
			item.Points = make([]publicMetricPoint, 0, len(points))
			for _, point := range points {
				item.Points = append(item.Points, publicMetricPoint{
					entityID: point.EntityID,
					Time:     point.Timestamp.UTC(),
					Value:    publicRawMetricValue(point.MetricName, point.Value, metricFillEmpty),
					Count:    1,
					Tags:     point.Tags,
					Labels:   point.Labels,
				})
			}
		} else {
			item.DownsampleAlgorithm = string(spec.algorithm)
			item.IntervalSeconds = spec.interval.Seconds()
			points := rollupValues[spec.metricKey][spec.algorithm]
			item.Points = make([]publicMetricPoint, 0, len(points))
			for _, point := range points {
				item.Points = append(item.Points, publicMetricPoint{
					entityID: point.EntityID,
					Time:     point.Bucket.UTC(),
					Value:    publicRawMetricValue(point.MetricName, point.Value, metricFillEmpty),
					Count:    point.Count,
					Tags:     point.Tags,
				})
			}
		}

		byEntity := make(map[string][]publicMetricSeries, len(entityIDs))
		for _, split := range splitPublicMetricSeries(item) {
			if split.EntityID != "" {
				byEntity[split.EntityID] = append(byEntity[split.EntityID], split)
			}
		}
		for _, entityID := range entityIDs {
			entitySeries := byEntity[entityID]
			if len(entitySeries) == 0 {
				empty := item
				empty.EntityID = entityID
				empty.Count = 0
				empty.Points = nil
				entitySeries = []publicMetricSeries{empty}
			}
			for _, split := range entitySeries {
				if metricFillEmpty {
					split = adaptiveFillPublicMetricSeries(split, start, end)
				}
				series = append(series, split)
			}
		}
	}

	return map[string]any{
		"start":                     start.UTC(),
		"end":                       end.UTC(),
		"server_downsample_default": true,
		"default_points":            defaultMetricQueryPoints,
		"series":                    series,
		"count":                     len(series),
	}, nil
}

type publicMetricPointResult struct {
	points      []publicMetricPoint
	downsampled bool
	interval    time.Duration
}

func loadPublicMetricPoints(
	ctx context.Context,
	store *metric.Store,
	query metric.Query,
	algorithm metric.Aggregation,
	maxPoints int,
	fillEmpty bool,
	now time.Time,
) (publicMetricPointResult, error) {
	if publicMetricUsesRawWindow(query.Start, query.End, now) {
		points, err := store.Query(ctx, query)
		if err != nil {
			return publicMetricPointResult{}, err
		}
		result := publicMetricPointResult{points: make([]publicMetricPoint, 0, len(points))}
		for _, point := range points {
			result.points = append(result.points, publicMetricPoint{
				entityID: point.EntityID,
				Time:     point.Timestamp.UTC(),
				Value:    publicRawMetricValue(point.MetricName, point.Value, fillEmpty),
				Count:    1,
				Tags:     point.Tags,
				Labels:   point.Labels,
			})
		}
		return result, nil
	}

	interval := metricDownsampleInterval(query.End.Sub(query.Start), maxPoints)
	interval = store.CompatibleSeriesInterval(query.Start, now, interval)
	points, err := store.Series(ctx, metric.AggregateQuery{
		Query:          query,
		Aggregation:    algorithm,
		Interval:       interval,
		PreserveSeries: true,
	}, now)
	if err != nil {
		return publicMetricPointResult{}, err
	}
	result := publicMetricPointResult{
		points:      make([]publicMetricPoint, 0, len(points)),
		downsampled: true,
		interval:    interval,
	}
	for _, point := range points {
		result.points = append(result.points, publicMetricPoint{
			entityID: point.EntityID,
			Time:     point.Bucket.UTC(),
			Value:    publicRawMetricValue(point.MetricName, point.Value, fillEmpty),
			Count:    point.Count,
			Tags:     point.Tags,
		})
	}
	return result, nil
}

func publicMetricUsesRawWindow(start, end, now time.Time) bool {
	retention := metricstore.DefaultRollupRawRetention
	return end.Sub(start) <= retention && end.After(now.UTC().Add(-retention))
}

func publicGetPingMetricStats(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	ctx, release, err := acquireHistoryQuery(ctx)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "history query canceled or timed out", nil)
	}
	defer release()
	var params publicPingMetricStatsParams
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid request body: "+err.Error(), nil)
	}

	end := metricQueryTimeOrDefault(params.End, time.Now().UTC())
	startFallback := end.Add(-metricQueryHours(params.Hours))
	start := metricQueryTimeOrDefault(params.Start, startFallback)
	if !end.After(start) {
		return nil, rpc.MakeError(rpc.InvalidParams, "end must be after start", nil)
	}

	requestedEntities := normalizeStringList(params.EntityIDs, []string{params.EntityID})
	entityIDs, rpcErr := publicMetricEntityIDs(ctx, requestedEntities)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if len(entityIDs) == 0 {
		return publicPingMetricStatsResponse{
			Start: start.UTC(),
			End:   end.UTC(),
			Stats: []publicPingMetricTaskStats{},
			Count: 0,
		}, nil
	}

	store := metricstore.GetStore()
	if store == nil {
		return nil, rpc.MakeError(rpc.InternalError, "metric store not initialized", nil)
	}

	taskList, err := tasks.GetAllPingTasks()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to fetch ping tasks: "+err.Error(), nil)
	}
	taskMap := make(map[string]models.PingTask, len(taskList))
	for _, task := range taskList {
		taskMap[strconv.FormatUint(uint64(task.Id), 10)] = task
	}
	taskFilter := normalizePingMetricTaskIDs(params.TaskID, params.TaskIDs)

	now := time.Now().UTC()
	// Use the finest tier still retained for the requested window. Statistics do
	// not depend on chart point budgets or on how output buckets are aligned.
	interval := store.CompatibleSeriesInterval(start, now, time.Minute)
	stats, err := loadPublicPingStats(ctx, store, entityIDs, start, end, interval, now, taskMap, taskFilter)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to query ping stats: "+err.Error(), nil)
	}

	sort.Slice(stats, func(i, j int) bool {
		if stats[i].EntityID != stats[j].EntityID {
			return stats[i].EntityID < stats[j].EntityID
		}
		return stats[i].TaskID < stats[j].TaskID
	})

	return publicPingMetricStatsResponse{
		Start:           start.UTC(),
		End:             end.UTC(),
		IntervalSeconds: interval.Seconds(),
		Stats:           stats,
		Count:           len(stats),
	}, nil
}

type publicMetricSeriesGroup struct {
	entityID string
	tagsKey  string
	tags     map[string]string
	points   []publicMetricPoint
}

func splitPublicMetricSeries(base publicMetricSeries) []publicMetricSeries {
	if len(base.Points) == 0 {
		base.Count = 0
		return []publicMetricSeries{base}
	}

	groups := make(map[string]*publicMetricSeriesGroup)
	order := make([]string, 0)
	for _, point := range base.Points {
		entityID := base.EntityID
		if point.entityID != "" {
			entityID = point.entityID
		}
		tags := point.Tags
		point.Tags = tags
		tagsKey := publicMetricTagsKey(tags)
		key := entityID + "\x00" + tagsKey
		group := groups[key]
		if group == nil {
			group = &publicMetricSeriesGroup{
				entityID: entityID,
				tagsKey:  tagsKey,
				tags:     clonePublicMetricTags(tags),
			}
			groups[key] = group
			order = append(order, key)
		}
		group.points = append(group.points, point)
	}

	sort.SliceStable(order, func(i, j int) bool {
		a := groups[order[i]]
		b := groups[order[j]]
		if a.entityID != b.entityID {
			return a.entityID < b.entityID
		}
		return a.tagsKey < b.tagsKey
	})

	out := make([]publicMetricSeries, 0, len(order))
	for _, key := range order {
		group := groups[key]
		item := base
		item.EntityID = group.entityID
		item.Tags = group.tags
		item.Points = group.points
		item.Count = len(group.points)
		out = append(out, item)
	}
	return out
}

// adaptiveFillPublicMetricSeries inserts only the null points needed to mark
// chart boundaries and real collection gaps. The typical collection interval
// is inferred per metric/entity/tag series, so sparse periodic data does not
// expand into hundreds of artificial empty buckets.
func adaptiveFillPublicMetricSeries(series publicMetricSeries, start, end time.Time) publicMetricSeries {
	pointTimes := make([]time.Time, len(series.Points))
	deltas := make([]time.Duration, 0, len(series.Points))
	for i, point := range series.Points {
		pointTimes[i] = point.Time
		if i > 0 {
			delta := point.Time.Sub(pointTimes[i-1])
			if delta > 0 {
				deltas = append(deltas, delta)
			}
		}
	}

	expectedInterval := time.Duration(series.IntervalSeconds * float64(time.Second))
	// Two deltas are the minimum needed to distinguish a regular cadence from
	// one isolated long gap. A lower quartile keeps outages from inflating the
	// inferred cadence when the rest of the series is regular.
	if len(deltas) >= 2 {
		sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
		observedInterval := deltas[(len(deltas)-1)/4]
		if observedInterval > expectedInterval {
			expectedInterval = observedInterval
		}
	}
	if expectedInterval > 0 {
		series.IntervalSeconds = expectedInterval.Seconds()
	}

	nullPoint := func(at time.Time) publicMetricPoint {
		return publicMetricPoint{
			Time:  at.UTC(),
			Value: nil,
			Tags:  series.Tags,
		}
	}
	filled := make([]publicMetricPoint, 0, len(series.Points)+2)
	if len(pointTimes) == 0 || start.Before(pointTimes[0]) {
		filled = append(filled, nullPoint(start))
	}
	for i, point := range series.Points {
		if i > 0 && expectedInterval > 0 && series.Points[i-1].Value != nil && point.Value != nil {
			delta := pointTimes[i].Sub(pointTimes[i-1])
			if delta > expectedInterval+expectedInterval/2 {
				filled = append(filled, nullPoint(pointTimes[i-1].Add(expectedInterval)))
			}
		}
		filled = append(filled, point)
	}
	// Do not append a trailing null after real data. It would make the final chart
	// bucket blank regardless of whether the tail is a collection delay or a gap.
	if len(pointTimes) == 0 {
		filled = append(filled, nullPoint(end))
	}
	series.Points = filled
	series.Count = len(filled)
	return series
}

func publicMetricTagsKey(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(tags[key])
		b.WriteByte('\x00')
	}
	return b.String()
}

func clonePublicMetricTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		out[key] = value
	}
	return out
}

func publicMetricEntityIDs(ctx context.Context, requested []string) ([]string, *rpc.JsonRpcError) {
	if len(requested) > 2048 {
		return nil, rpc.MakeError(rpc.InvalidParams, "at most 2048 entities can be queried at once", nil)
	}
	allClients, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to retrieve client information: "+err.Error(), nil)
	}
	isLogin := isLoginFromCtx(ctx)
	hidden := make(map[string]bool, len(allClients))
	visible := make(map[string]bool, len(allClients))
	var allVisible []string
	for _, client := range allClients {
		if client.Hidden {
			hidden[client.UUID] = true
		}
		if client.Hidden && !isLogin {
			continue
		}
		visible[client.UUID] = true
		allVisible = append(allVisible, client.UUID)
	}
	if len(requested) == 0 {
		return allVisible, nil
	}
	out := make([]string, 0, len(requested))
	for _, entityID := range requested {
		if hidden[entityID] && !isLogin {
			continue
		}
		if visible[entityID] || !hidden[entityID] {
			out = append(out, entityID)
		}
	}
	return out, nil
}

func normalizeStringList(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, group := range groups {
		for _, item := range group {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func metricQueryTimeOrDefault(value *time.Time, fallback time.Time) time.Time {
	if value == nil {
		return fallback.UTC()
	}
	return value.UTC()
}

func metricQueryHours(hours float64) time.Duration {
	if hours <= 0 {
		return 4 * time.Hour
	}
	return time.Duration(hours * float64(time.Hour))
}

func resolveMetricMaxPoints(metricKey string, params publicMetricQueryParams) (int, error) {
	maxPoints := params.MaxPoints
	if maxPoints == 0 {
		maxPoints = defaultMetricQueryPoints
	}
	if v, ok := params.MaxPointsByMetric[metricKey]; ok {
		maxPoints = v
	}
	if maxPoints <= 0 || maxPoints > maxPublicMetricPoints {
		return 0, fmt.Errorf("max points for %s must be between 1 and %d", metricKey, maxPublicMetricPoints)
	}
	return maxPoints, nil
}

func resolveMetricAggregation(metricKey string, params publicMetricQueryParams) metric.Aggregation {
	raw := params.Aggregation
	if v := firstNonEmpty(
		params.AggregationByMetric[metricKey],
	); v != "" {
		raw = v
	}
	if raw == "" {
		raw = string(metric.AggAvg)
	}
	return metric.Aggregation(strings.ToLower(strings.TrimSpace(raw)))
}

func resolveMetricFillEmpty(params publicMetricQueryParams) bool {
	return params.FillEmpty != nil && *params.FillEmpty
}

func publicMetricValue(value float64) *float64 {
	return &value
}

func publicRawMetricValue(metricName string, value float64, fillEmpty bool) *float64 {
	if isNullPingMetricValue(metricName, value, fillEmpty) {
		return nil
	}
	return publicMetricValue(value)
}

func isNullPingMetricValue(metricName string, value float64, fillEmpty bool) bool {
	if !fillEmpty || value != -1 {
		return false
	}
	return metricName == metricstore.MetricPingLatency || metricName == metricstore.MetricPingLoss
}

// loadPublicPingStats merges source distributions once for the entire window.
func loadPublicPingStats(ctx context.Context, store *metric.Store, entityIDs []string, start, end time.Time, interval time.Duration, now time.Time, taskMap map[string]models.PingTask, taskFilter map[string]bool) ([]publicPingMetricTaskStats, error) {
	loaded, err := store.SeriesBatch(ctx, metric.BatchSeriesQuery{
		Specs: []metric.BatchSeriesSpec{
			{MetricName: metricstore.MetricPingLatency, Aggregations: []metric.Aggregation{metric.AggAvg, metric.AggMin, metric.AggMax, metric.AggLast, metric.AggP95, metric.AggStdDev}, Interval: interval, PreserveSeries: true, WholeWindow: true},
			{MetricName: metricstore.MetricPingLoss, Aggregations: []metric.Aggregation{metric.AggSum}, Interval: interval, PreserveSeries: true, WholeWindow: true},
		}, EntityIDs: entityIDs, Start: start, End: end, Order: metric.OrderAsc,
	}, now)
	if err != nil {
		return nil, err
	}
	type key struct{ entity, task string }
	losses := make(map[key]metric.Distribution)
	for _, d := range loaded.Summaries[metricstore.MetricPingLoss] {
		losses[key{d.EntityID, d.Tags["task_id"]}] = d
	}
	out := make([]publicPingMetricTaskStats, 0)
	for _, d := range loaded.Summaries[metricstore.MetricPingLatency] {
		taskID := d.Tags["task_id"]
		if taskID == "" || d.Count == 0 || (len(taskFilter) > 0 && !taskFilter[taskID]) {
			continue
		}
		loss, ok := losses[key{d.EntityID, taskID}]
		stat := publicPingStatsFromDistribution(d, loss, ok)
		if task, ok := taskMap[taskID]; ok {
			stat.Name = task.Name
			stat.Type = task.Type
			stat.Interval = task.Interval
		}
		out = append(out, stat)
	}
	return out, nil
}

func publicPingStatsFromDistribution(d, loss metric.Distribution, hasLoss bool) publicPingMetricTaskStats {
	stat := publicPingMetricTaskStats{EntityID: d.EntityID, TaskID: d.Tags["task_id"], Tags: d.Tags, Total: d.Count, QuantilesApproximate: true}
	// A missing/mismatched loss stream cannot reconstruct successful moments.
	// Report unknown latency rather than treating failed probes as fast samples.
	if d.Count <= 0 || d.Min < -1 || !hasLoss || loss.Count != d.Count || loss.Sum < 0 || loss.Sum > float64(d.Count) {
		stat.LossApproximate = true
		return stat
	}
	lost := int(math.Round(loss.Sum))
	stat.Valid = d.Count - lost
	stat.Loss = float64(lost) / float64(d.Count) * 100
	if stat.Valid == 0 {
		return stat
	}
	// Failed probes are stored as -1 in latency and 1 in loss. Remove their
	// exact contributions from the moments before calculating population SD.
	avg := (d.Sum + float64(lost)) / float64(stat.Valid)
	variance := (d.SumSquares-float64(lost))/float64(stat.Valid) - avg*avg
	stat.Avg = publicMetricValue(avg)
	stat.StdDev = publicMetricValue(math.Sqrt(math.Max(0, variance)))
	if d.Min >= 0 {
		stat.Min = publicMetricValue(d.Min)
	}
	if d.Max >= 0 {
		stat.Max = publicMetricValue(d.Max)
	}
	// If the last probe failed, latest is unavailable; don't substitute an average.
	if d.Last >= 0 {
		stat.Latest = publicMetricValue(d.Last)
	}
	quantile := func(q float64) *float64 {
		rank := (float64(lost) + q*float64(stat.Valid)) / float64(d.Count)
		return publicMetricValue(math.Max(0, d.Quantile(rank)))
	}
	stat.P50 = quantile(.50)
	stat.P95 = quantile(.95)
	stat.P99 = quantile(.99)
	if *stat.P50 > 0 && *stat.P99 >= *stat.P50 {
		stat.P99P50Ratio = (*stat.P99 - *stat.P50) / math.Max(math.Min(*stat.P50, 50), 10)
	}
	return stat
}

func normalizePingMetricTaskIDs(taskID any, taskIDs []any) map[string]bool {
	out := make(map[string]bool)
	add := func(value any) {
		switch v := value.(type) {
		case nil:
			return
		case string:
			if raw := strings.TrimSpace(v); raw != "" {
				out[raw] = true
			}
		case float64:
			out[strconv.FormatInt(int64(v), 10)] = true
		case int:
			out[strconv.Itoa(v)] = true
		case int64:
			out[strconv.FormatInt(v, 10)] = true
		case jsonNumber:
			if raw := strings.TrimSpace(v.String()); raw != "" {
				out[raw] = true
			}
		default:
			raw := strings.TrimSpace(fmt.Sprint(v))
			if raw != "" {
				out[raw] = true
			}
		}
	}
	add(taskID)
	for _, value := range taskIDs {
		add(value)
	}
	return out
}

type jsonNumber interface {
	String() string
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func metricDownsampleInterval(rangeDuration time.Duration, maxPoints int) time.Duration {
	if maxPoints <= 0 {
		maxPoints = defaultMetricQueryPoints
	}
	nanos := rangeDuration.Nanoseconds()
	if nanos <= 0 {
		return time.Second
	}
	interval := time.Duration((nanos-1)/int64(maxPoints) + 1)
	if interval < time.Second {
		return time.Second
	}
	return metric.CeilStandardInterval(interval)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
