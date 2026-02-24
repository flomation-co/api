package main

import (
	"flomation.app/automate/api/internal/actions"
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

	log.Info("running action migrations")
	m, err := actions.NewMigrator(cfg)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create migration service")
		return
	}
	result, err := m.Migrate("actions-manifest.json", true)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to apply action migrations")
		return
	}

	if result != nil {
		log.WithFields(log.Fields{
			"created": result.Created,
			"updated": result.Updated,
			"removed": result.Removed,
		}).Info("action migration result")
	}

	db, err := persistence.NewService(cfg)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create persistence service")
	}

	httpService := http.NewService(cfg, db)

	log.Fatal(httpService.Listen())
}
