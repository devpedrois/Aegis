package metrics

import "time"

type MetricsSnapshot struct {
	Timestamp      time.Time
	TotalReqPerSec float64
	BackendStats   []BackendSnapshot
	BlockedIPs     int
	CurrentRate    float64
	AdaptiveState  string
}

type BackendSnapshot struct {
	URL            string
	Healthy        bool
	CircuitState   string
	ReqPerSec      float64
	LatencyP50     time.Duration
	LatencyP95     time.Duration
	LatencyP99     time.Duration
	ActiveRequests int64
	TotalRequests  int64
	TotalErrors    int64
	ErrorRate      float64
}

type WindowSnapshot struct {
	Count      int
	ErrorCount int
	ReqPerSec  float64
	ErrorRate  float64
	P50        time.Duration
	P95        time.Duration
	P99        time.Duration
}
