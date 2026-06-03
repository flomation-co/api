package metrics

import (
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
)

// ── Counters (incremented inline by handlers) ────────────────────────

// ExecutionsTotal is incremented when an execution completes.
var ExecutionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "flomation_executions_total",
	Help: "Total executions completed since service start.",
}, []string{"status"})

// ── Gauges (updated by the periodic collector) ───────────────────────

var (
	executionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flomation_executions_active",
		Help: "Currently running executions.",
	})
	flowsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flomation_flows_total",
		Help: "Total non-archived flows.",
	})
	agentsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flomation_agents_active",
		Help: "Agents with status running.",
	})
	usersActiveDaily = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flomation_users_active_daily",
		Help: "Users active in the last 24 hours.",
	})
	executionMinutesMonth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flomation_execution_minutes_month",
		Help: "Total execution minutes in the current calendar month.",
	})
	organisationsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flomation_organisations_total",
		Help: "Total number of organisations on the platform.",
	})
)

// StartCollector launches a background goroutine that periodically
// queries the database to update gauge metrics.
func StartCollector(db *sqlx.DB, interval time.Duration) {
	go func() {
		time.Sleep(5 * time.Second)
		collect(db)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			collect(db)
		}
	}()
	log.WithField("interval", interval).Info("metrics collector started")
}

func collect(db *sqlx.DB) {
	var count int64

	if err := db.Get(&count, `SELECT COUNT(*) FROM execution WHERE execution_status = 'running'`); err == nil {
		executionsActive.Set(float64(count))
	}

	if err := db.Get(&count, `SELECT COUNT(*) FROM flo WHERE archived_at IS NULL`); err == nil {
		flowsTotal.Set(float64(count))
	}

	if err := db.Get(&count, `SELECT COUNT(*) FROM agent WHERE status = 'running'`); err == nil {
		agentsActive.Set(float64(count))
	}

	if err := db.Get(&count, `SELECT COUNT(*) FROM users WHERE last_activity_at > NOW() - INTERVAL '24 hours'`); err == nil {
		usersActiveDaily.Set(float64(count))
	}

	// Total execution minutes in the current calendar month (from billing_duration).
	var totalMs int64
	if err := db.Get(&totalMs, `
		SELECT COALESCE(SUM(billing_duration), 0)
		FROM execution
		WHERE created_at >= date_trunc('month', NOW())
		  AND billing_duration IS NOT NULL`); err == nil {
		executionMinutesMonth.Set(float64(totalMs) / 60000.0)
	}

	// Total organisations.
	if err := db.Get(&count, `SELECT COUNT(*) FROM organisation`); err == nil {
		organisationsTotal.Set(float64(count))
	}
}
