package metrics

import (
	"math"
	"sort"
	"time"
)

func Percentile(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}

	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	return percentileFromSorted(sorted, p)
}

func percentileFromSorted(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}

	index := int(math.Ceil(float64(len(sorted))*p/100.0)) - 1
	if index < 0 {
		index = 0
	}

	if index >= len(sorted) {
		index = len(sorted) - 1
	}

	return sorted[index]
}

func P50(durations []time.Duration) time.Duration {
	return Percentile(durations, 50)
}

func P95(durations []time.Duration) time.Duration {
	return Percentile(durations, 95)
}

func P99(durations []time.Duration) time.Duration {
	return Percentile(durations, 99)
}
