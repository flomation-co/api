package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getFloFavourites(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	favourites, err := s.persistence.GetFloFavourites(user.ID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get favourites")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(favourites) == 0 {
		c.JSON(http.StatusOK, []string{})
		return
	}

	c.JSON(http.StatusOK, favourites)
}

func (s *Service) addFloFavourite(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	floID := c.Param("FloID")

	if err := s.persistence.AddFloFavourite(user.ID, floID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to add favourite")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusCreated)
}

func (s *Service) removeFloFavourite(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	floID := c.Param("FloID")

	if err := s.persistence.RemoveFloFavourite(user.ID, floID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to remove favourite")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}
