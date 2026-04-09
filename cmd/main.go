package main

import (
	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/http"
	"flomation.app/automate/api/internal/persistence"
	"flomation.app/automate/api/internal/version"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func main() {
	gin.SetMode(gin.ReleaseMode)

	log.WithFields(log.Fields{
		"version": version.Version,
		"hash":    version.GetHash(),
		"date":    version.BuiltDate,
	}).Info("Starting Flomation API Server")

	cfg, err := config.LoadConfig("config.json")
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to load config")
		return
	}

	log.Info("running database migrations")
	if err := persistence.CheckAndUpdate(cfg); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to run database migrations")
		return
	}

	db, err := persistence.NewService(cfg)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create persistence service")
	}

	// Phase 2d-γ: seed the canonical extraction System Flow if it
	// doesn't already exist, and backfill extraction_flow_id for any
	// agents that were created before the seed ran. Idempotent — safe
	// to run on every startup.
	if err := db.BootstrapExtractionFlow(); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Warn("extraction flow bootstrap failed (non-fatal, extraction will be disabled)")
	}

	// Background reaper for stuck/stale executions. Cancels zombies
	// (running > 5 min) and stale system flow queue entries (created > 10 min).
	db.StartExecutionReaper()

	httpService := http.NewService(cfg, db)

	log.Fatal(httpService.Listen())
}
