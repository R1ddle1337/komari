package metricstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/komari-monitor/komari/pkg/metric"
)

func TestDefaultRollupPolicy(t *testing.T) {
	policy := defaultRollupPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatalf("default rollup policy should validate: %v", err)
	}
	if policy.RawRetention != DefaultRollupRawRetention {
		t.Fatalf("raw retention = %s, want %s", policy.RawRetention, DefaultRollupRawRetention)
	}
	if len(policy.Tiers) != 4 {
		t.Fatalf("expected 4 rollup tiers, got %d", len(policy.Tiers))
	}

	wantIntervals := []time.Duration{time.Minute, 5 * time.Minute, time.Hour, 24 * time.Hour}
	wantRetentions := []time.Duration{600 * time.Minute, 600 * 5 * time.Minute, 600 * time.Hour, 100 * 365 * 24 * time.Hour}
	for i := range wantIntervals {
		if policy.Tiers[i].Interval != wantIntervals[i] {
			t.Fatalf("tier %d interval = %s, want %s", i, policy.Tiers[i].Interval, wantIntervals[i])
		}
		if policy.Tiers[i].Retention != wantRetentions[i] {
			t.Fatalf("tier %d retention = %s, want %s", i, policy.Tiers[i].Retention, wantRetentions[i])
		}
	}
}

func TestBuildMetricConfigEnablesDefaultRollupPolicy(t *testing.T) {
	cfg, err := buildMetricConfig(&MetricStoreConfig{
		Driver:      "sqlite",
		DSN:         ":memory:",
		TablePrefix: "metric_",
	}, false)
	if err != nil {
		t.Fatalf("build metric config: %v", err)
	}
	if !cfg.RollupPolicy.Enabled() {
		t.Fatal("expected default rollup policy to be enabled")
	}
	if cfg.RollupPolicy.RawRetention != DefaultRollupRawRetention {
		t.Fatalf("raw retention = %s, want %s", cfg.RollupPolicy.RawRetention, DefaultRollupRawRetention)
	}
	if cfg.SQLite.ReadPoolSize != 2 {
		t.Fatalf("metric store read pool = %d, want fixed size 2", cfg.SQLite.ReadPoolSize)
	}
}

func TestBuildMetricConfigLeavesFinalRetentionToMetricDefinition(t *testing.T) {
	cfg, err := buildMetricConfig(&MetricStoreConfig{
		Driver: "sqlite",
		DSN:    ":memory:",
	}, false)
	if err != nil {
		t.Fatalf("build metric config: %v", err)
	}
	wantRollupRetention := 100 * 365 * 24 * time.Hour
	lastTier := cfg.RollupPolicy.Tiers[len(cfg.RollupPolicy.Tiers)-1]
	if lastTier.Retention != wantRollupRetention {
		t.Fatalf("rollup retention = %s, want %s", lastTier.Retention, wantRollupRetention)
	}
}

func TestBuildMetricConfigUsesCustomRollupRetention(t *testing.T) {
	cfg, err := buildMetricConfig(&MetricStoreConfig{
		Driver:                           "sqlite",
		DSN:                              ":memory:",
		RollupMinuteRetentionMinutes:     30,
		RollupFiveMinuteRetentionMinutes: 150,
		RollupHourRetentionHours:         300,
	}, false)
	if err != nil {
		t.Fatalf("build metric config: %v", err)
	}

	want := []time.Duration{30 * time.Minute, 150 * time.Minute, 300 * time.Hour}
	if len(cfg.RollupPolicy.Tiers) != 4 {
		t.Fatalf("tier count = %d, want 4", len(cfg.RollupPolicy.Tiers))
	}
	for i, retention := range want {
		if cfg.RollupPolicy.Tiers[i].Retention != retention {
			t.Fatalf("tier %d retention = %s, want %s", i, cfg.RollupPolicy.Tiers[i].Retention, retention)
		}
	}
}

func TestBuildMetricConfigRejectsInvalidRollupRetention(t *testing.T) {
	tests := []MetricStoreConfig{
		{
			Driver:                       "sqlite",
			DSN:                          ":memory:",
			RollupMinuteRetentionMinutes: -1,
		},
		{
			Driver:                           "sqlite",
			DSN:                              ":memory:",
			RollupMinuteRetentionMinutes:     120,
			RollupFiveMinuteRetentionMinutes: 60,
			RollupHourRetentionHours:         600,
		},
		{
			Driver:                           "sqlite",
			DSN:                              ":memory:",
			RollupMinuteRetentionMinutes:     30,
			RollupFiveMinuteRetentionMinutes: 150,
			RollupHourRetentionHours:         1,
		},
	}
	for i, cfg := range tests {
		if _, err := buildMetricConfig(&cfg, false); err == nil {
			t.Fatalf("case %d: expected invalid rollup retention error", i)
		}
	}
}

func TestBuildMetricConfigDefaultsOmittedRollupRetention(t *testing.T) {
	cfg, err := buildMetricConfig(&MetricStoreConfig{Driver: "sqlite", DSN: ":memory:"}, false)
	if err != nil {
		t.Fatalf("build metric config: %v", err)
	}
	if got, want := cfg.RollupPolicy.Tiers[0].Retention, 600*time.Minute; got != want {
		t.Fatalf("minute retention = %s, want %s", got, want)
	}
	if got, want := cfg.RollupPolicy.Tiers[1].Retention, 3000*time.Minute; got != want {
		t.Fatalf("five-minute retention = %s, want %s", got, want)
	}
}

func TestConfigFromFingerprintPreservesRollupRetention(t *testing.T) {
	base := &MetricStoreConfig{
		TablePrefix:                      "metrics_",
		MaxOpenConns:                     11,
		MaxIdleConns:                     4,
		RollupMinuteRetentionMinutes:     30,
		RollupFiveMinuteRetentionMinutes: 150,
		RollupHourRetentionHours:         300,
	}

	got, err := configFromFingerprint("mysql|user:password@tcp(host:3306)/metrics", base)
	if err != nil {
		t.Fatalf("config from fingerprint: %v", err)
	}
	if got.RollupMinuteRetentionMinutes != base.RollupMinuteRetentionMinutes ||
		got.RollupFiveMinuteRetentionMinutes != base.RollupFiveMinuteRetentionMinutes ||
		got.RollupHourRetentionHours != base.RollupHourRetentionHours {
		t.Fatalf("rollup retention was not preserved: %#v", got)
	}
}

func TestBuildMetricConfigAlwaysEnablesDownsampling(t *testing.T) {
	cfg, err := buildMetricConfig(&MetricStoreConfig{
		Driver: "sqlite",
		DSN:    ":memory:",
	}, false)
	if err != nil {
		t.Fatalf("build metric config: %v", err)
	}
	if !cfg.RollupPolicy.Enabled() {
		t.Fatal("expected rollup policy to be enabled")
	}
}

func TestCreateMetricDefinitionsUsesExplicitRetentionAndPreservesOverrides(t *testing.T) {
	if defaultBuiltinMetricRetentionDays != 1 {
		t.Fatalf("default built-in metric retention = %d, want 1 day", defaultBuiltinMetricRetentionDays)
	}

	ctx := context.Background()
	s, err := metric.Open(ctx, metric.SQLite(":memory:", metric.WithMaxOpenConns(1)))
	if err != nil {
		t.Fatalf("open metric store: %v", err)
	}
	defer s.Close()

	if err := createMetricDefinitions(ctx, s); err != nil {
		t.Fatalf("create definitions: %v", err)
	}
	defs, err := s.ListMetrics(ctx)
	if err != nil {
		t.Fatalf("list definitions: %v", err)
	}
	if len(defs) != 21 {
		t.Fatalf("definition count = %d, want 21", len(defs))
	}
	for _, def := range defs {
		if def.RetentionDays != defaultBuiltinMetricRetentionDays {
			t.Fatalf("%s retention = %d, want %d", def.Name, def.RetentionDays, defaultBuiltinMetricRetentionDays)
		}
	}

	cpu, err := s.GetMetric(ctx, MetricCPU)
	if err != nil {
		t.Fatalf("get cpu definition: %v", err)
	}
	cpu.RetentionDays = 60
	if err := s.UpsertMetric(ctx, cpu); err != nil {
		t.Fatalf("override cpu retention: %v", err)
	}
	if err := createMetricDefinitions(ctx, s); err != nil {
		t.Fatalf("recreate definitions: %v", err)
	}
	cpu, err = s.GetMetric(ctx, MetricCPU)
	if err != nil {
		t.Fatalf("reload cpu definition: %v", err)
	}
	if cpu.RetentionDays != 60 {
		t.Fatalf("cpu retention = %d, want preserved override 60", cpu.RetentionDays)
	}
	if _, err := s.SetMetricRetention(ctx, MetricCPU, 0); err != nil {
		t.Fatalf("disable cpu retention: %v", err)
	}
	if err := createMetricDefinitions(ctx, s); err != nil {
		t.Fatalf("refresh disabled definition: %v", err)
	}
	cpu, err = s.GetMetric(ctx, MetricCPU)
	if err != nil {
		t.Fatalf("reload disabled cpu definition: %v", err)
	}
	if cpu.RetentionDays != 0 {
		t.Fatalf("cpu retention = %d, want preserved disabled state", cpu.RetentionDays)
	}
}

func TestCreateMetricDefinitionsKeepsExistingMetrics(t *testing.T) {
	ctx := context.Background()
	s, err := metric.Open(ctx, metric.SQLite(":memory:", metric.WithMaxOpenConns(1)))
	if err != nil {
		t.Fatalf("open metric store: %v", err)
	}
	defer s.Close()
	if err := s.CreateMetric(ctx, metric.Definition{Name: "memory.total", Type: metric.TypeGauge, RetentionDays: 1}); err != nil {
		t.Fatalf("create obsolete definition: %v", err)
	}
	if err := s.Write(ctx, metric.Point{MetricName: "memory.total", EntityID: "node-a", Timestamp: time.Now().UTC(), Value: 1024}); err != nil {
		t.Fatalf("write obsolete point: %v", err)
	}
	if err := createMetricDefinitions(ctx, s); err != nil {
		t.Fatalf("refresh built-in definitions: %v", err)
	}
	definition, err := s.GetMetric(ctx, "memory.total")
	if err != nil {
		t.Fatalf("existing definition was removed: %v", err)
	}
	if definition.RetentionDays != 1 {
		t.Fatalf("existing retention = %d, want 1", definition.RetentionDays)
	}
	points, err := s.Query(ctx, metric.Query{MetricName: "memory.total", EntityID: "node-a", Start: time.Now().UTC().Add(-time.Hour), End: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatalf("query existing points: %v", err)
	}
	if len(points) != 1 || points[0].Value != 1024 {
		t.Fatalf("existing points were removed: %#v", points)
	}
}

func TestCreateMetricDefinitionsUsesLegacySpanOnlyForNewDefinitions(t *testing.T) {
	ctx := context.Background()
	s, err := metric.Open(ctx, metric.SQLite(":memory:", metric.WithMaxOpenConns(1)))
	if err != nil {
		t.Fatalf("open metric store: %v", err)
	}
	defer s.Close()

	if err := createMetricDefinitionsWithDefaultRetention(ctx, s, 10); err != nil {
		t.Fatalf("create migration definitions: %v", err)
	}
	defs, err := s.ListMetrics(ctx)
	if err != nil {
		t.Fatalf("list migration definitions: %v", err)
	}
	for _, def := range defs {
		if def.RetentionDays != 10 {
			t.Fatalf("%s retention = %d, want legacy span 10", def.Name, def.RetentionDays)
		}
	}

	cpu, err := s.GetMetric(ctx, MetricCPU)
	if err != nil {
		t.Fatalf("get CPU definition: %v", err)
	}
	cpu.RetentionDays = 3
	if err := s.UpsertMetric(ctx, cpu); err != nil {
		t.Fatalf("override CPU retention: %v", err)
	}
	if err := createMetricDefinitionsWithDefaultRetention(ctx, s, 20); err != nil {
		t.Fatalf("refresh migration definitions: %v", err)
	}
	cpu, err = s.GetMetric(ctx, MetricCPU)
	if err != nil {
		t.Fatalf("reload CPU definition: %v", err)
	}
	if cpu.RetentionDays != 3 {
		t.Fatalf("existing CPU retention = %d, want preserved 3", cpu.RetentionDays)
	}
}

func TestGetRetentionSummaryUsesAllMetricDefinitions(t *testing.T) {
	ctx := context.Background()
	s, err := metric.Open(ctx, metric.SQLite(":memory:", metric.WithMaxOpenConns(1)))
	if err != nil {
		t.Fatalf("open metric store: %v", err)
	}
	defer s.Close()

	storeMu.Lock()
	oldStore := store
	store = s
	storeMu.Unlock()
	defer func() {
		storeMu.Lock()
		store = oldStore
		storeMu.Unlock()
	}()

	empty, err := GetRetentionSummary(ctx)
	if err != nil {
		t.Fatalf("summarize empty store: %v", err)
	}
	if empty.AllPositive || empty.MaxDays != 0 {
		t.Fatalf("unexpected empty summary: %#v", empty)
	}
	for _, def := range []metric.Definition{
		{Name: "short", Type: metric.TypeGauge, RetentionDays: 7},
		{Name: "long", Type: metric.TypeGauge, RetentionDays: 60},
	} {
		if err := s.UpsertMetric(ctx, def); err != nil {
			t.Fatalf("upsert %s: %v", def.Name, err)
		}
	}
	summary, err := GetRetentionSummary(ctx)
	if err != nil {
		t.Fatalf("summarize definitions: %v", err)
	}
	if !summary.AllPositive || summary.MaxDays != 60 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if _, err := s.SetMetricRetention(ctx, "short", 0); err != nil {
		t.Fatalf("disable short metric: %v", err)
	}
	summary, err = GetRetentionSummary(ctx)
	if err != nil {
		t.Fatalf("summarize disabled metric: %v", err)
	}
	if summary.AllPositive || summary.MaxDays != 60 {
		t.Fatalf("unexpected disabled summary: %#v", summary)
	}
}

func TestSummarizeRetentionDefinitionsRequiresEveryMetricToBePositive(t *testing.T) {
	summary := summarizeRetentionDefinitions([]metric.Definition{
		{Name: "enabled", RetentionDays: 30},
		{Name: "disabled", RetentionDays: 0},
		{Name: "long", RetentionDays: 60},
	})
	if summary.AllPositive || summary.MaxDays != 60 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestCompactCleansPointsOutsideFixedRawWindow(t *testing.T) {
	ctx := context.Background()
	s, err := metric.Open(ctx, metric.SQLite(":memory:", metric.WithMaxOpenConns(1), metric.WithRollupPolicy(metric.RollupPolicy{})))
	if err != nil {
		t.Fatalf("open metric store: %v", err)
	}
	if err := s.UpsertMetric(ctx, metric.Definition{
		Name:          "raw.metric",
		Type:          metric.TypeGauge,
		RetentionDays: 1,
	}); err != nil {
		t.Fatalf("upsert metric: %v", err)
	}

	now := time.Now().UTC()
	if err := s.WriteBatch(ctx, []metric.Point{
		{MetricName: "raw.metric", EntityID: "node", Timestamp: now.Add(-11 * time.Minute), Value: 1},
		{MetricName: "raw.metric", EntityID: "node", Timestamp: now.Add(-30 * time.Second), Value: 2},
	}); err != nil {
		t.Fatalf("write points: %v", err)
	}

	storeMu.Lock()
	oldStore := store
	store = s
	storeMu.Unlock()
	defer func() {
		storeMu.Lock()
		store = oldStore
		storeMu.Unlock()
		_ = s.Close()
	}()

	if _, err := Compact(ctx, now); err != nil {
		t.Fatalf("compact: %v", err)
	}
	points, err := s.Query(ctx, metric.Query{
		MetricName: "raw.metric",
		EntityID:   "node",
		Start:      now.Add(-time.Hour),
		End:        now,
	})
	if err != nil {
		t.Fatalf("query points: %v", err)
	}
	if len(points) != 1 || points[0].Value != 2 {
		t.Fatalf("expected only the retained raw point, got %#v", points)
	}
}

func TestRetentionCleanupReportsDeleteFailure(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "compact.db")
	s, err := metric.Open(ctx, metric.SQLite(dsn,
		metric.WithMaxOpenConns(1),
		metric.WithRollupPolicy(defaultRollupPolicy()),
	))
	if err != nil {
		t.Fatalf("open metric store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, name := range []string{"a.invalid", "b.healthy"} {
		if err := s.CreateMetric(ctx, metric.Definition{Name: name, Type: metric.TypeGauge, RetentionDays: 1}); err != nil {
			t.Fatalf("create metric %s: %v", name, err)
		}
	}

	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	if err := s.WriteBatch(ctx, []metric.Point{
		{MetricName: "a.invalid", EntityID: "node", Timestamp: old, Value: 1},
		{MetricName: "b.healthy", EntityID: "node", Timestamp: old, Value: 2},
	}); err != nil {
		t.Fatalf("write compact fixtures: %v", err)
	}

	rawDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite connection: %v", err)
	}
	_, err = rawDB.ExecContext(ctx, `CREATE TRIGGER fail_invalid_metric_rollup
		BEFORE DELETE ON metric_rollups
		WHEN OLD.series_id IN (SELECT id FROM metric_series WHERE metric_name = 'a.invalid')
		BEGIN SELECT RAISE(FAIL, 'forced compact failure'); END`)
	_ = rawDB.Close()
	if err != nil {
		t.Fatalf("create compact failure trigger: %v", err)
	}

	storeMu.Lock()
	previousStore := store
	store = s
	storeMu.Unlock()
	t.Cleanup(func() {
		storeMu.Lock()
		store = previousStore
		storeMu.Unlock()
	})

	if _, err := CleanupExpired(ctx, now); err == nil {
		t.Fatal("expected retention cleanup to report the forced delete failure")
	}
	points, err := s.Query(ctx, metric.Query{
		MetricName: "b.healthy",
		EntityID:   "node",
		Start:      old.Add(-time.Minute),
		End:        now,
	})
	if err != nil {
		t.Fatalf("query healthy rollups: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("retention cleanup unexpectedly changed healthy metric data: %#v", points)
	}
}
