package http

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"net/http"
	"time"
)

func (s *Service) runnerMiddleware(c *gin.Context) {
	c.Next()
}

func (s *Service) getExecutorDownloadLink(c *gin.Context) {
	s3path := fmt.Sprintf("%v/%v", s.config.Executor.Path, s.config.Executor.Filename)

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to load default aws config")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	client := s3.NewFromConfig(cfg)
	s3service := s3.NewPresignClient(client)

	req, err := s3service.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(s.config.Executor.Bucket),
		Key:    aws.String(s3path),
	}, func(options *s3.PresignOptions) {
		options.Expires = time.Duration(time.Minute * 15)
	})
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to presign request")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"url": req.URL,
	})
}
