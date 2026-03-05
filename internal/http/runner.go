package http

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) verifyPayload(runnerKey string, c *gin.Context) error {
	var key *rsa.PublicKey

	block, rest := pem.Decode([]byte(runnerKey))
	if block == nil {
		return errors.New("unable to decode pem block")
	}

	if len(rest) > 0 {
		log.Warn("trailing data after runner public key")
	}

	switch block.Type {
	case "PUBLIC KEY":
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return err
		}
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return err
		}

		key = k
	case "RSA PUBLIC KEY":
		pub, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return err
		}

		key = pub

	default:
		return errors.New("invalid block type")
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return err
	}

	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	hash := sha256.Sum256(body)

	header := c.GetHeader("X-Flomation-Runner-Signature")
	if header == "" {
		return errors.New("missing signature header")
	}

	headerDecoded, err := hex.DecodeString(header)
	if err != nil {
		return err
	}

	if err = rsa.VerifyPSS(key, crypto.SHA256, hash[:], headerDecoded, nil); err != nil {
		return err
	}

	return nil
}

func (s *Service) executionMiddleware(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	execution, err := s.persistence.GetExecutionByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get execution")
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if execution.RunnerID == nil {
		log.Error("exeuction is not assigned a runner, can't verify identity")
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	r, err := s.persistence.GetRunnerByID(*execution.RunnerID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get runner")
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if r == nil {
		log.WithFields(log.Fields{
			"id": *execution.RunnerID,
		}).Error("unable to locate runner")
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if r.PublicKey != nil {
		if err := s.verifyPayload(*r.PublicKey, c); err != nil {
			log.WithFields(log.Fields{
				"error": err,
				"key":   *r.PublicKey,
			}).Error("unable to verify payload")
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}

	c.Next()
}

func (s *Service) runnerMiddleware(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	r, err := s.persistence.GetRunnerByIdentifier(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get runner")
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if r == nil {
		log.WithFields(log.Fields{
			"id": id,
		}).Error("unable to locate runner")
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if r.PublicKey != nil {
		if err := s.verifyPayload(*r.PublicKey, c); err != nil {
			log.WithFields(log.Fields{
				"error": err,
				"key":   *r.PublicKey,
			}).Error("unable to verify payload")
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}

	c.Next()
}

func (s *Service) unregisterRunner(c *gin.Context) {
	c.AbortWithStatus(http.StatusNotImplemented)
}

func (s *Service) getRunners(c *gin.Context) {
	runners, err := s.persistence.GetRunners()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get runners")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(runners) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, runners)
}

func (s *Service) registerRunner(c *gin.Context) {
	var request api.Runner

	if err := c.BindJSON(&request); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	r, err := s.persistence.GetRunnerByIdentifier(request.Identifier)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to check existing runner")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if r != nil {
		if err := s.persistence.UpdateRunnerLastContact(r.ID, c.ClientIP()); err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to update existing runner")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		if len(request.Manifest) > 0 {
			result, err := s.migrator.Migrate(request.Manifest, true)
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
		}

		c.Status(http.StatusCreated)
		return
	}

	q, err := s.persistence.GetQueueByRegistrationCode(request.RegistrationCode)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to find queue")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if q == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	id, err := s.persistence.EnrolRunner(request)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to enrol runner")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if id == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	result, err := s.migrator.Migrate(request.Manifest, true)
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

	runner, err := s.persistence.GetRunnerByID(*id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
			"id":    id,
		}).Error("unable to get runner")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, runner)
}

func (s *Service) checkForRunnerExecutions(c *gin.Context) {
	id := c.Param("id")

	runner, err := s.persistence.GetRunnerByIdentifier(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("invalid runner")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if runner == nil {
		log.WithFields(log.Fields{
			"id": id,
		}).Error("invalid runner ID")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdateRunnerLastContact(runner.ID, c.ClientIP()); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update runner last contact")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if !runner.Active {
		c.Status(http.StatusNoContent)
		return
	}

	execution, err := s.persistence.GetExecutionForRunnerID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to check for execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if execution == nil {
		c.Status(http.StatusNoContent)
		return
	}

	flow, err := s.persistence.GetFloByID(execution.FloID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to load flow")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	rev, err := s.persistence.GetLatestRevisionByFloID(flow.ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get latest revision for Flo")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if rev != nil {
		if rev.Data != nil {
			var revision interface{}

			if err := json.Unmarshal(rev.Data.([]byte), &revision); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to unmarshal revision data")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}

			rev.Data = revision
		}
	}

	if err := s.persistence.UpdateExecutionStatus(execution.ID, "allocated"); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to mark execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdateExecutionRunnerID(execution.ID, runner.ID); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to execution runner ID")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	pe := api.PendingExecution{
		Flow:      *flow,
		Execution: *execution,
		Data:      rev.Data,
	}

	c.JSON(http.StatusOK, pe)
}
