package internal

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type Metrics struct {
	requestsTotal   *counterVec
	requestDuration *histogramVec
	dbQuery         func() (walletCount, ledgerDepth int64, err error)
	startTime       time.Time
	mu              sync.RWMutex
}

type counterVec struct {
	m sync.Map
}

func newCounterVec() *counterVec {
	return &counterVec{m: sync.Map{}}
}

func (c *counterVec) Inc(labels ...string) {
	key := fmt.Sprintf("%v", labels)
	val, _ := c.m.LoadOrStore(key, &atomic.Int64{})
	val.(*atomic.Int64).Add(1)
}

func (c *counterVec) Describe() map[string]int64 {
	out := make(map[string]int64)
	c.m.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}

type histogramVec struct {
	m sync.Map
}

func newHistogramVec() *histogramVec {
	return &histogramVec{m: sync.Map{}}
}

func (h *histogramVec) Observe(duration time.Duration, labels ...string) {
	key := fmt.Sprintf("%v", labels)
	buckets := []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	val, _ := h.m.LoadOrStore(key, make([]int64, len(buckets)+1))
	b := val.([]int64)
	for i, bound := range buckets {
		if duration.Seconds() <= bound {
			b[i]++
			return
		}
	}
	b[len(buckets)]++
}

func NewMetrics(dbQuery func() (int64, int64, error)) *Metrics {
	return &Metrics{
		requestsTotal:   newCounterVec(),
		requestDuration: newHistogramVec(),
		dbQuery:         dbQuery,
		startTime:       time.Now().UTC(),
	}
}

func (m *Metrics) Record(method, path, status string, start time.Time, fn func()) {
	fn()
	duration := time.Since(start)
	m.requestsTotal.Inc(method, path, status)
	m.requestDuration.Observe(duration, method, path)
}

func (m *Metrics) PrometheusText() string {
	var out string
	out += fmt.Sprintf("# HELP pocketledger_uptime_seconds Server uptime\n# TYPE pocketledger_uptime_seconds gauge\npocketledger_uptime_seconds %.0f\n\n",
		time.Since(m.startTime).Seconds())

	out += "# HELP pocketledger_requests_total Total HTTP requests\n# TYPE pocketledger_requests_total counter\n"
	for key, val := range m.requestsTotal.Describe() {
		out += fmt.Sprintf("pocketledger_requests_total{%s} %d\n", key, val)
	}
	out += "\n"

	if m.dbQuery != nil {
		wallets, entries, err := m.dbQuery()
		status := "ok"
		if err != nil {
			status = "error"
		}
		out += fmt.Sprintf("# HELP pocketledger_wallets_count Current wallet count\n# TYPE pocketledger_wallets_count gauge\npocketledger_wallets_count{status=\"%s\"} %d\n\n", status, wallets)
		out += fmt.Sprintf("# HELP pocketledger_ledger_depth Total ledger entries\n# TYPE pocketledger_ledger_depth gauge\npocketledger_ledger_depth %d\n\n", entries)
	}
	return out
}
