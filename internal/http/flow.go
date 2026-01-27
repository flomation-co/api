package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getMyFlos(c *gin.Context) {
	user := s.getUserFromContext(c)

	offsetQuery := c.DefaultQuery("offset", "0")
	limitQuery := c.DefaultQuery("limit", "10")
	searchQuery := c.DefaultQuery("search", "")

	offset, err := strconv.ParseInt(offsetQuery, 10, 64)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to parse offset")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	limit, err := strconv.ParseInt(limitQuery, 10, 64)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to parse offset")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	flos, count, err := s.persistence.GetMyFlos(user.ID, offset, limit, searchQuery)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get flos")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(flos) == 0 {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	c.Writer.Header().Set("x-total-items", fmt.Sprintf("%v", count))

	c.JSON(http.StatusOK, flos)
}

func (s *Service) getFloByID(c *gin.Context) {
	ID := c.Param("FloID")

	flo, err := s.persistence.GetFloByID(ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get flo by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if flo == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	revision, err := s.persistence.GetLatestRevisionByFloID(flo.ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get latest revision")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if revision != nil {
		if revision.Data != nil {
			var r interface{}

			if err := json.Unmarshal(revision.Data.([]byte), &r); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to unmarshal revision data")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}

			revision.Data = r
		}

		flo.LatestRevision = revision
	}

	c.JSON(http.StatusOK, flo)
}

func (s *Service) createFlo(c *gin.Context) {
	var flo api.Flo
	if err := c.BindJSON(&flo); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind JSON")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	user := s.getUserFromContext(c)
	flo.AuthorID = &user.ID

	id, err := s.persistence.CreateFlo(flo)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create flo")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	f, err := s.persistence.GetFloByID(*id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get flo by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, f)
}

func (s *Service) updateFlo(c *gin.Context) {
	ID := c.Param("FloID")

	var flo api.Flo
	if err := c.BindJSON(&flo); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind JSON")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	flo.ID = ID

	if err := s.persistence.UpdateFlo(flo); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create flo")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	f, err := s.persistence.GetFloByID(ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get flo by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, f)
}

func (s *Service) deleteFlo(c *gin.Context) {
	ID := c.Param("FloID")

	flo, err := s.persistence.GetFloByID(ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get flo by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if flo == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if err := s.persistence.DeleteFlo(*flo); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to delete flo by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Service) createFloRevision(c *gin.Context) {
	FloID := c.Param("FloID")

	var revision api.Revision
	if err := c.BindJSON(&revision); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	revision.FloID = FloID

	j, err := json.Marshal(revision.Data)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to marshal data")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	revision.Data = j

	id, err := s.persistence.CreateFloRevision(revision)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create flo revision")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"revision_id": id,
	})
}

func (s *Service) triggerFlo(c *gin.Context) {
	triggerID := c.Param("TriggerID")
	floID := c.Param("FloID")

	var data interface{}
	err := c.ShouldBindJSON(&data)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind payload")
	}

	i, err := s.persistence.TriggerExecution(floID, triggerID, data)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to trigger execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id": i,
	})
}
