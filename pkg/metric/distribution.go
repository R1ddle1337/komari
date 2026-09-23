package metric

import (
	"math"
	"time"
)

// Distribution contains full-window moments and a merged quantile sketch.
// Moments and extrema cover the selected retained rollup buckets; quantiles
// are estimates from their t-digests. Entity and tag boundaries are preserved.
type Distribution struct {
	EntityID                        string
	Tags                            map[string]string
	Count                           int
	Sum, SumSquares, Min, Max, Last float64
	LastTime                        time.Time
	digest                          *TDigest
}

func (d Distribution) Quantile(q float64) float64 {
	if d.digest == nil {
		return math.NaN()
	}
	return d.digest.Quantile(q)
}

func (a *metricSeriesAccumulator) distributions() []Distribution {
	keys := make([]rollupKey, 0, len(a.groups))
	for key := range a.groups {
		keys = append(keys, key)
	}
	sortRollupKeys(keys)
	out := make([]Distribution, 0, len(keys))
	for _, key := range keys {
		state := a.groups[key]
		b := state.summary
		out = append(out, Distribution{EntityID: key.entityID, Tags: state.tags, Count: int(b.count), Sum: b.sum, SumSquares: b.sumSq, Min: b.min, Max: b.max, Last: b.lastVal, LastTime: fromMillis(b.lastTS), digest: b.digest})
	}
	return out
}
