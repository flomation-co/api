package http

import (
	"net/http"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getMyOrganisations(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	orgs, err := s.persistence.GetMyOrganisations(user.ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get user organisations")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(orgs) == 0 {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, orgs)
}

func (s *Service) getOrganisation(c *gin.Context) {
	ID := c.Param("ID")
	org, err := s.persistence.GetOrganisationByID(ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get organisation by ID")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, org)
}

func (s *Service) createOrganisation(c *gin.Context) {
	var org api.Organisation
	if err := c.BindJSON(&org); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	id, err := s.persistence.CreateOrganisation(org)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create organisation")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.AddUserToOrganisation(*id, s.getUserFromContext(c).ID); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to add user to organisation")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	o, err := s.persistence.GetOrganisationByID(*id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get organisation by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, o)
}

func (s *Service) updateOrganisation(c *gin.Context) {
	var org api.Organisation
	if err := c.BindJSON(&org); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdateOrganisation(org); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update organisation")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	o, err := s.persistence.GetOrganisationByID(org.ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get organisation by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, o)
}
