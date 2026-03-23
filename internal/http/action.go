package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// categoryMetadata maps the first path segment of an action ID to its display metadata.
var categoryMetadata = map[string]api.ActionCategory{
	"arithmetic":  {Key: "arithmetic", Name: "Arithmetic", Icon: "calculator", Description: "Mathematical operations"},
	"aws":         {Key: "aws", Name: "AWS", Icon: "cloud", Description: "Amazon Web Services integrations"},
	"common":      {Key: "common", Name: "Common", Icon: "toolbox", Description: "General-purpose data utilities"},
	"conditional": {Key: "conditional", Name: "Conditional", Icon: "code-branch", Description: "Control flow based on conditions"},
	"git":         {Key: "git", Name: "Git", Icon: "code-branch", Description: "Version control operations"},
	"output":      {Key: "output", Name: "Output", Icon: "location-arrow", Description: "Send data to external destinations"},
	"security":    {Key: "security", Name: "Security", Icon: "shield-halved", Description: "Security scanning and compliance"},
	"nosql":       {Key: "nosql", Name: "NoSQL", Icon: "layer-group", Description: "NoSQL database operations"},
	"sql":         {Key: "sql", Name: "SQL", Icon: "database", Description: "Relational database queries"},
	"script":      {Key: "script", Name: "Script", Icon: "terminal", Description: "Execute scripts and commands"},
	"trigger":     {Key: "trigger", Name: "Triggers", Icon: "bolt-lightning", Description: "Start a Flow"},
}

// subCategoryMetadata maps sub-paths (e.g. "aws/s3") to display metadata.
var subCategoryMetadata = map[string]struct {
	Name        string
	Icon        string
	Description string
}{
	"aws/s3":  {Name: "S3", Icon: "box-archive", Description: "Simple Storage Service operations"},
	"aws/ec2": {Name: "EC2", Icon: "server", Description: "Elastic Compute Cloud operations"},
}

func getCategoryForAction(actionID string) *api.ActionCategory {
	parts := strings.Split(actionID, "/")
	if len(parts) == 0 {
		return nil
	}
	cat, ok := categoryMetadata[parts[0]]
	if !ok {
		return nil
	}

	// For 3+ segment action IDs, populate sub-category fields
	if len(parts) >= 3 {
		subPath := parts[0] + "/" + parts[1]
		if sub, ok := subCategoryMetadata[subPath]; ok {
			cat.SubKey = subPath
			cat.SubName = sub.Name
			cat.SubIcon = sub.Icon
			cat.SubDescription = sub.Description
		} else {
			// Auto-generate from directory name
			cat.SubKey = subPath
			cat.SubName = strings.ToUpper(parts[1][:1]) + parts[1][1:]
		}
	}

	return &cat
}

func (s *Service) getActions(c *gin.Context) {
	actions, err := s.persistence.GetActions()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get actions")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	mappedActions := make(map[string]api.Action)

	for _, a := range actions {
		a.Type, _ = strconv.ParseInt(a.ActionType, 10, 64)

		if a.Inputs != nil {
			var inputs []api.InputDefinition
			if err := json.Unmarshal(a.Inputs.([]byte), &inputs); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to get actions")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			a.Inputs = inputs
		}

		if a.Outputs != nil {
			var outputs []api.OutputDefinition
			if err := json.Unmarshal(a.Outputs.([]byte), &outputs); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to get actions")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			a.Outputs = outputs
		}

		a.Category = getCategoryForAction(a.ID)
		mappedActions[a.ID] = *a
	}

	c.JSON(http.StatusOK, mappedActions)
}
