package internal

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type counterVec struct {
	mu   sync.RWMutex
	data map[string]int64
}

func newCounterVec() *counterVec {
	return &counterVec{
		data: make(map[string]int64),
	}
}

func (c *counterVec) Inc(labels map[string]string) {
	key := labelsKey(labels)

	c.mu.Lock()
	c.data[key]++
	c.mu.Unlock()
}

func (c *counterVec) Describe() map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make(map[string]int64, len(c.data))
	for k, v := range c.data {
		out[k] = v
	}

	return out
}

type histogramVec struct {
	mu   sync.RWMutex
	data map[string][]float64
}

func newHistogramVec() *histogramVec {
	return &histogramVec{
		data: make(map[string][]float64),
	}
}

func (h *histogramVec) Observe(labels map[string]string, value float64) {
	key := labelsKey(labels)

	h.mu.Lock()
	h.data[key] = append(h.data[key], value)
	h.mu.Unlock()
}

func labelsKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))

	for k := range labels {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(keys))

	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, labels[k]))
	}

	return strings.Join(parts, ",")
}

type Metrics struct {
	requestsTotal   *counterVec
	requestDuration *histogramVec
	dbQuery         func() (walletCount, ledgerDepth int64, err error)
	startTime       time.Time
	mu              sync.RWMutex

	// Reconciliation metrics
	unreconciledGauge atomic.Int64 // current count of unreconciled entries
	reconciledTotal   atomic.Int64 // total successful reconciliations
	reconciledErrors  atomic.Int64 // total failed reconciliation attempts
	lastReconcileRun  time.Time    // last time the reconciler ran
	muReconcile       sync.RWMutex // protects lastReconcileRun
}

func NewMetrics() *Metrics {
	return &Metrics{
		requestsTotal:   newCounterVec(),
		requestDuration: newHistogramVec(),
		startTime:       time.Now().UTC(),
	}
}

func (m *Metrics) SetDBQuery(fn func() (walletCount, ledgerDepth int64, err error)) {
	m.mu.Lock()
	m.dbQuery = fn
	m.mu.Unlock()
}

func (m *Metrics) Record(method, path string, duration time.Duration) {
	if m.requestsTotal != nil {
		m.requestsTotal.Inc(map[string]string{
			"method": method,
			"path":   path,
		})
	}

	if m.requestDuration != nil {
		m.requestDuration.Observe(map[string]string{
			"method": method,
			"path":   path,
		}, duration.Seconds())
	}
}

// Gauge helpers

func (m *Metrics) SetUnreconciledCount(count int64) {
	m.unreconciledGauge.Store(count)
}

func (m *Metrics) IncReconciled() {
	m.reconciledTotal.Add(1)
}

func (m *Metrics) IncReconciledErrors() {
	m.reconciledErrors.Add(1)
}

func (m *Metrics) RecordReconcileRun() {
	m.muReconcile.Lock()
	m.lastReconcileRun = time.Now().UTC()
	m.muReconcile.Unlock()
}

func (m *Metrics) PrometheusText() string {
	var out string

	out += fmt.Sprintf(
		"# HELP pocketledger_uptime_seconds Server uptime\n"+
			"# TYPE pocketledger_uptime_seconds gauge\n"+
			"pocketledger_uptime_seconds %.0f\n\n",
		time.Since(m.startTime).Seconds(),
	)

	out += "# HELP pocketledger_requests_total Total HTTP requests\n"
	out += "# TYPE pocketledger_requests_total counter\n"

	if m.requestsTotal != nil {
		for key, val := range m.requestsTotal.Describe() {
			out += fmt.Sprintf(
				"pocketledger_requests_total{%s} %d\n",
				key,
				val,
			)
		}
	}

	out += "\n"

	if m.dbQuery != nil {
		wallets, entries, err := m.dbQuery()

		status := "ok"
		if err != nil {
			status = "error"
		}

		out += fmt.Sprintf(
			"# HELP pocketledger_wallets_count Current wallet count\n"+
				"# TYPE pocketledger_wallets_count gauge\n"+
				"pocketledger_wallets_count{status=\"%s\"} %d\n\n",
			status,
			wallets,
		)

		out += fmt.Sprintf(
			"# HELP pocketledger_ledger_depth Total ledger entries\n"+
				"# TYPE pocketledger_ledger_depth gauge\n"+
				"pocketledger_ledger_depth %d\n\n",
			entries,
		)
	}

	// Reconciliation metrics

	out += fmt.Sprintf(
		"# HELP pocketledger_unreconciled_entries Number of ledger entries needing reconciliation\n"+
			"# TYPE pocketledger_unreconciled_entries gauge\n"+
			"pocketledger_unreconciled_entries %d\n\n",
		m.unreconciledGauge.Load(),
	)

	out += fmt.Sprintf(
		"# HELP pocketledger_reconciled_total Total successful reconciliations\n"+
			"# TYPE pocketledger_reconciled_total counter\n"+
			"pocketledger_reconciled_total %d\n\n",
		m.reconciledTotal.Load(),
	)

	out += fmt.Sprintf(
		"# HELP pocketledger_reconciled_errors_total Total failed reconciliation attempts\n"+
			"# TYPE pocketledger_reconciled_errors_total counter\n"+
			"pocketledger_reconciled_errors_total %d\n\n",
		m.reconciledErrors.Load(),
	)

	m.muReconcile.RLock()
	lastRun := m.lastReconcileRun
	m.muReconcile.RUnlock()

	if !lastRun.IsZero() {
		out += fmt.Sprintf(
			"# HELP pocketledger_last_reconcile_run_seconds Timestamp of last reconciler run\n"+
				"# TYPE pocketledger_last_reconcile_run_seconds gauge\n"+
				"pocketledger_last_reconcile_run_seconds %d\n\n",
			lastRun.Unix(),
		)
	}

	return out
}
