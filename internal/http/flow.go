package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
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

	var orgID string
	if len(user.Organisations) > 0 {
		orgID = user.Organisations[0].ID
	}

	flos, count, err := s.persistence.GetMyFlos(user.ID, offset, limit, searchQuery, orgID)
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

	// Load action definitions once to cross-reference required fields
	actions, _ := s.persistence.GetActions()
	actionInputs := buildRequiredFieldsMap(actions)

	// Compute validation errors for each flo by inspecting latest revision
	for _, flo := range flos {
		flo.HasValidationErrors = checkFloValidation(s, flo.ID, actionInputs)
	}

	c.Writer.Header().Set("x-total-items", fmt.Sprintf("%v", count))

	c.JSON(http.StatusOK, flos)
}

func (s *Service) getFloByID(c *gin.Context) {
	ID := c.Param("FloID")
	user := s.getUserFromContext(c)

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

	if !s.verifyOrgAccess(user, flo.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
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
	if !s.checkPermission(c, rbac.FlowCreate) {
		return
	}

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

	if len(user.Organisations) > 0 {
		flo.OrganisationID = &user.Organisations[0].ID
	}

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

	// Register default trigger(s) with Launch Service
	for _, t := range f.Triggers {
		if err := s.launch.RegisterTrigger(t.ID, t.TypeName, nil, f.ID, s.extractAuthToken(c)); err != nil {
			log.WithFields(log.Fields{
				"error":      err,
				"trigger_id": t.ID,
				"flo_id":     f.ID,
			}).Warn("unable to register trigger with launch service")
		}
	}

	c.JSON(http.StatusCreated, f)
}

func (s *Service) updateFlo(c *gin.Context) {
	if !s.checkPermission(c, rbac.FlowEdit) {
		return
	}

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
	if !s.checkPermission(c, rbac.FlowDelete) {
		return
	}

	ID := c.Param("FloID")
	user := s.getUserFromContext(c)

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

	if !s.verifyOrgAccess(user, flo.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
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
	if !s.checkPermission(c, rbac.FlowEdit) {
		return
	}

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

	// Parse revision data to extract trigger nodes before marshalling
	var revisionData struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Data struct {
				ID     string `json:"id"`
				Label  string `json:"label"`
				Config struct {
					Type   int64                    `json:"type"`
					Plugin string                   `json:"plugin"`
					Inputs []map[string]interface{} `json:"inputs"`
				} `json:"config"`
			} `json:"data"`
		} `json:"nodes"`
	}

	rawData, _ := json.Marshal(revision.Data)
	_ = json.Unmarshal(rawData, &revisionData)

	// Enforce single On Error handler per flow
	var onErrorCount int
	for _, node := range revisionData.Nodes {
		if node.Type == "error/on_error" || node.Data.Label == "error/on_error" {
			onErrorCount++
		}
	}
	if onErrorCount > 1 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Only one On Error handler is allowed per flow",
		})
		return
	}

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

	// Sync trigger nodes with API triggers and Launch service
	user := s.getUserFromContext(c)
	authToken := ""
	if tkn := s.getTokenFromContext(c); tkn != nil {
		authToken = *tkn
	}

	// Get existing triggers for this flow to avoid duplicates
	existingTriggers, _ := s.persistence.GetTriggersByFloID(FloID)

	for _, node := range revisionData.Nodes {
		label := node.Data.Label
		if label == "" {
			continue
		}

		// Check if this is a non-manual trigger node
		isTrigger := node.Data.Config.Type == 1 || strings.HasPrefix(label, "trigger/")
		isManual := label == "trigger/manual"
		if !isTrigger || isManual {
			continue
		}

		// Map the trigger type name
		typeName := strings.TrimPrefix(label, "trigger/")
		typeName = strings.ReplaceAll(typeName, "_", "-")

		// Build trigger data from node inputs (unresolved — Launch resolves at poll time)
		triggerData := make(map[string]interface{})
		for _, input := range node.Data.Config.Inputs {
			name, _ := input["name"].(string)
			value := input["value"]
			if name != "" && value != nil {
				triggerData[name] = value
			}
		}

		// Store the flow node ID so the executor can be started from the correct entry node
		triggerData["__node_id"] = node.ID

		// For form triggers, extract and parse form_definition as the root trigger data
		if typeName == "form" {
			if fd, ok := triggerData["form_definition"]; ok {
				if fdStr, ok := fd.(string); ok && fdStr != "" {
					var formDef map[string]interface{}
					if err := json.Unmarshal([]byte(fdStr), &formDef); err == nil {
						triggerData = formDef
					}
				}
			}
		}

		// Check if a trigger of this type already exists for this flow
		var existingID *string
		for _, et := range existingTriggers {
			if et.TypeName == typeName {
				existingID = &et.ID
				break
			}
		}

		if existingID != nil {
			triggerData["id"] = *existingID

			// Update existing trigger data
			trigger := api.Trigger{
				ID:       *existingID,
				Name:     label,
				TypeName: typeName,
				FloID:    &FloID,
				Data:     triggerData,
			}

			if err := s.persistence.UpdateTrigger(trigger); err != nil {
				log.WithFields(log.Fields{
					"error":      err,
					"trigger_id": *existingID,
				}).Warn("unable to update trigger from revision node")
			}

			s.registerTriggerWithLaunch(*existingID, trigger, authToken)
		} else {
			// Create new trigger
			trigger := api.Trigger{
				Name:     label,
				TypeName: typeName,
				FloID:    &FloID,
				Data:     triggerData,
			}

			if user != nil {
				trigger.OwnerID = &user.ID
				if len(user.Organisations) > 0 {
					trigger.OrganisationID = &user.Organisations[0].ID
				}
			}

			triggerID, err := s.persistence.CreateTriggerWithType(trigger)
			if err != nil {
				log.WithFields(log.Fields{
					"error": err,
					"type":  typeName,
				}).Warn("unable to create trigger from revision node")
				continue
			}

			if err := s.persistence.LinkFloToTrigger(FloID, *triggerID); err != nil {
				log.WithFields(log.Fields{
					"error":      err,
					"trigger_id": *triggerID,
					"flo_id":     FloID,
				}).Warn("unable to link trigger to flow")
			}

			s.registerTriggerWithLaunch(*triggerID, trigger, authToken)

			log.WithFields(log.Fields{
				"trigger_id": *triggerID,
				"type":       typeName,
				"flo_id":     FloID,
			}).Info("registered trigger from flow revision")
		}
	}

	// Remove triggers that are no longer in the revision
	// Collect type names of triggers found in this revision
	revisionTriggerTypes := make(map[string]bool)
	for _, node := range revisionData.Nodes {
		label := node.Data.Label
		if label == "" {
			continue
		}
		isTrigger := node.Data.Config.Type == 1 || strings.HasPrefix(label, "trigger/")
		isManual := label == "trigger/manual"
		if isTrigger && !isManual {
			tn := strings.TrimPrefix(label, "trigger/")
			tn = strings.ReplaceAll(tn, "_", "-")
			revisionTriggerTypes[tn] = true
		}
	}

	for _, et := range existingTriggers {
		if et.TypeName == "manual" {
			continue
		}
		if !revisionTriggerTypes[et.TypeName] {
			// Trigger was removed from the flow — disable in Launch and delete from API
			if err := s.launch.DisableTrigger(et.ID, authToken); err != nil {
				log.WithFields(log.Fields{
					"error":      err,
					"trigger_id": et.ID,
				}).Warn("unable to disable removed trigger in launch service")
			}

			if err := s.persistence.DeleteTrigger(et.ID); err != nil {
				log.WithFields(log.Fields{
					"error":      err,
					"trigger_id": et.ID,
				}).Warn("unable to delete removed trigger")
			}

			log.WithFields(log.Fields{
				"trigger_id": et.ID,
				"type":       et.TypeName,
				"flo_id":     FloID,
			}).Info("removed trigger no longer in flow revision")
		}
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

	// Wake any long-polling runners
	s.executionNotifier.Notify()

	c.JSON(http.StatusCreated, gin.H{
		"id": i,
	})
}

func (s *Service) executeFlo(c *gin.Context) {
	floID := c.Param("FloID")

	// Find the manual trigger for this flow
	triggers, err := s.persistence.GetTriggersByFloID(floID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get triggers for flow")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var data map[string]interface{}
	_ = c.ShouldBindJSON(&data)

	// Determine channel type from trigger data (if agent-dispatched)
	channelType, _ := data["channel_type"].(string)

	var triggerID string

	// First: try to match by channel type → trigger type (e.g. "slack" → trigger type "slack")
	if channelType != "" {
		for _, t := range triggers {
			if t.TypeName == channelType {
				triggerID = t.ID
				break
			}
		}
	}

	// Second: try manual trigger
	if triggerID == "" {
		for _, t := range triggers {
			if t.TypeName == "manual" {
				triggerID = t.ID
				break
			}
		}
	}

	// Fall back to any trigger
	if triggerID == "" && len(triggers) > 0 {
		triggerID = triggers[0].ID
	}

	if triggerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "flow has no trigger"})
		return
	}

	i, err := s.persistence.TriggerExecution(floID, triggerID, data)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to trigger execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// If this execution was agent-dispatched, tag it with the agent and session IDs
	if i != nil && data != nil {
		if agentID, ok := data["agent_id"].(string); ok && agentID != "" {
			if err := s.persistence.SetExecutionAgentID(*i, agentID); err != nil {
				log.WithFields(log.Fields{
					"error":        err,
					"execution_id": *i,
					"agent_id":     agentID,
				}).Warn("unable to set execution agent id")
			}
			// Link to the active session
			if session, _ := s.persistence.GetActiveAgentSession(agentID); session != nil {
				if err := s.persistence.SetExecutionAgentSessionID(*i, session.ID); err != nil {
					log.WithFields(log.Fields{
						"error":        err,
						"execution_id": *i,
						"session_id":   session.ID,
					}).Warn("unable to set execution agent session id")
				}
			}
		}
	}

	// Wake any long-polling runners
	s.executionNotifier.Notify()

	c.JSON(http.StatusCreated, gin.H{
		"id": i,
	})
}

func (s *Service) exportFlos(c *gin.Context) {
	user := s.getUserFromContext(c)

	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.BindJSON(&req); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to bind export request")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids array is required"})
		return
	}

	var results []gin.H
	for _, id := range req.IDs {
		flo, err := s.persistence.GetFloByID(id)
		if err != nil {
			log.WithFields(log.Fields{"error": err, "flo_id": id}).Error("unable to get flo for export")
			continue
		}
		if flo == nil {
			continue
		}

		if !s.verifyOrgAccess(user, flo.OrganisationID) {
			continue
		}

		revision, err := s.persistence.GetLatestRevisionByFloID(flo.ID)
		if err != nil {
			log.WithFields(log.Fields{"error": err, "flo_id": id}).Error("unable to get revision for export")
			continue
		}

		var revisionData interface{}
		if revision != nil && revision.Data != nil {
			var r interface{}
			if err := json.Unmarshal(revision.Data.([]byte), &r); err == nil {
				revisionData = r
			}
		}

		results = append(results, gin.H{
			"id":               flo.ID,
			"name":             flo.Name,
			"scale":            flo.Scale,
			"x":                flo.XPosition,
			"y":                flo.YPosition,
			"environment_name": flo.EnvironmentName,
			"revision":         revisionData,
		})
	}

	if len(results) == 0 {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, results)
}

func (s *Service) importFlo(c *gin.Context) {
	if !s.checkPermission(c, rbac.FlowCreate) {
		return
	}

	var wrapper struct {
		FlomationExport struct {
			Version        int    `json:"version"`
			ExportedAt     string `json:"exported_at"`
			SourceFlowID   string `json:"source_flow_id"`
			SourceFlowName string `json:"source_flow_name"`
			AuthorEmail    string `json:"author_email"`
			Hash           string `json:"hash"`
		} `json:"flomation_export"`
		FlowData json.RawMessage `json:"flow_data"`
	}

	if err := c.BindJSON(&wrapper); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to bind import request")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Validate required fields
	if wrapper.FlomationExport.Version == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing flomation_export metadata"})
		return
	}

	if len(wrapper.FlowData) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing flow_data"})
		return
	}

	// Verify SHA-256 hash
	hash := sha256.Sum256(wrapper.FlowData)
	computedHash := hex.EncodeToString(hash[:])
	if computedHash != wrapper.FlomationExport.Hash {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hash verification failed — file may have been tampered with"})
		return
	}

	// Parse flow data
	var flowData struct {
		Name     string      `json:"name"`
		Scale    float32     `json:"scale"`
		X        float32     `json:"x"`
		Y        float32     `json:"y"`
		Revision interface{} `json:"revision"`
	}
	if err := json.Unmarshal(wrapper.FlowData, &flowData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid flow_data structure"})
		return
	}

	user := s.getUserFromContext(c)

	// Create new flow with importing user as author
	flo := api.Flo{
		Name:      flowData.Name,
		Scale:     flowData.Scale,
		XPosition: flowData.X,
		YPosition: flowData.Y,
	}
	flo.AuthorID = &user.ID
	if len(user.Organisations) > 0 {
		flo.OrganisationID = &user.Organisations[0].ID
	}

	floID, err := s.persistence.CreateFlo(flo)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to create imported flo")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Create initial revision if we have revision data
	if flowData.Revision != nil {
		revisionJSON, err := json.Marshal(flowData.Revision)
		if err != nil {
			log.WithFields(log.Fields{"error": err}).Error("unable to marshal imported revision")
		} else {
			revision := api.Revision{
				FloID: *floID,
				Data:  revisionJSON,
			}

			if _, err := s.persistence.CreateFloRevision(revision); err != nil {
				log.WithFields(log.Fields{"error": err}).Error("unable to create imported revision")
			}
		}
	}

	// Register default trigger(s) with Launch service
	f, err := s.persistence.GetFloByID(*floID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get imported flo")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	for _, t := range f.Triggers {
		if err := s.launch.RegisterTrigger(t.ID, t.TypeName, nil, f.ID, s.extractAuthToken(c)); err != nil {
			log.WithFields(log.Fields{
				"error":      err,
				"trigger_id": t.ID,
				"flo_id":     f.ID,
			}).Warn("unable to register trigger with launch service for imported flo")
		}
	}

	// Run trigger sync from revision data (same as createFloRevision)
	if flowData.Revision != nil {
		s.syncImportedTriggers(*floID, flowData.Revision, user, s.extractAuthToken(c))
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":               f.ID,
		"name":             f.Name,
		"created_at":       f.CreatedAt,
		"imported":         true,
		"imported_at":      time.Now().UTC().Format(time.RFC3339),
		"source_flow_id":   wrapper.FlomationExport.SourceFlowID,
		"source_flow_name": wrapper.FlomationExport.SourceFlowName,
	})
}

// syncImportedTriggers extracts non-manual trigger nodes from imported revision
// data and creates corresponding trigger records.
func (s *Service) syncImportedTriggers(floID string, revisionData interface{}, user *api.User, authToken string) {
	raw, err := json.Marshal(revisionData)
	if err != nil {
		return
	}

	var parsed struct {
		Nodes []struct {
			ID   string `json:"id"`
			Data struct {
				Label  string `json:"label"`
				Config struct {
					Type   int64                    `json:"type"`
					Inputs []map[string]interface{} `json:"inputs"`
				} `json:"config"`
			} `json:"data"`
		} `json:"nodes"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		return
	}

	for _, node := range parsed.Nodes {
		label := node.Data.Label
		if label == "" {
			continue
		}

		isTrigger := node.Data.Config.Type == 1 || strings.HasPrefix(label, "trigger/")
		isManual := label == "trigger/manual"
		if !isTrigger || isManual {
			continue
		}

		typeName := strings.TrimPrefix(label, "trigger/")
		typeName = strings.ReplaceAll(typeName, "_", "-")

		triggerData := make(map[string]interface{})
		for _, input := range node.Data.Config.Inputs {
			name, _ := input["name"].(string)
			value := input["value"]
			if name != "" && value != nil {
				triggerData[name] = value
			}
		}
		triggerData["__node_id"] = node.ID

		trigger := api.Trigger{
			Name:     label,
			TypeName: typeName,
			FloID:    &floID,
			Data:     triggerData,
		}

		if user != nil {
			trigger.OwnerID = &user.ID
			if len(user.Organisations) > 0 {
				trigger.OrganisationID = &user.Organisations[0].ID
			}
		}

		triggerID, err := s.persistence.CreateTriggerWithType(trigger)
		if err != nil {
			continue
		}

		_ = s.persistence.LinkFloToTrigger(floID, *triggerID)
		s.registerTriggerWithLaunch(*triggerID, trigger, authToken)
	}
}

// buildRequiredFieldsMap parses action definitions to find which inputs
// are required per action type. Returns map[actionID]map[inputName]bool.
func buildRequiredFieldsMap(actions []*api.Action) map[string]map[string]bool {
	result := make(map[string]map[string]bool)
	for _, a := range actions {
		if a.Inputs == nil {
			continue
		}
		raw, ok := a.Inputs.([]byte)
		if !ok {
			continue
		}
		var inputs []api.InputDefinition
		if err := json.Unmarshal(raw, &inputs); err != nil {
			continue
		}
		fields := make(map[string]bool)
		for _, inp := range inputs {
			if inp.Required {
				fields[inp.Name] = true
			}
		}
		if len(fields) > 0 {
			result[a.ID] = fields
		}
	}
	return result
}

// checkFloValidation inspects the latest revision to see if any node
// has required inputs with empty values. Cross-references with action
// definitions since older revisions may not have the required flag stored.
func checkFloValidation(s *Service, floID string, actionInputs map[string]map[string]bool) bool {
	rev, err := s.persistence.GetLatestRevisionByFloID(floID)
	if err != nil || rev == nil || rev.Data == nil {
		return false
	}

	var raw []byte
	switch d := rev.Data.(type) {
	case []byte:
		raw = d
	case string:
		raw = []byte(d)
	default:
		return false
	}

	var revData map[string]interface{}
	if err := json.Unmarshal(raw, &revData); err != nil {
		return false
	}

	nodes, ok := revData["nodes"].([]interface{})
	if !ok {
		return false
	}

	for _, n := range nodes {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}

		// Get the node's action type (e.g. "sql/postgresql")
		nodeType, _ := node["type"].(string)

		data, ok := node["data"].(map[string]interface{})
		if !ok {
			continue
		}
		config, ok := data["config"].(map[string]interface{})
		if !ok {
			continue
		}
		inputs, ok := config["inputs"].([]interface{})
		if !ok {
			continue
		}

		// Get required fields from action definitions as fallback
		requiredFromDef := actionInputs[nodeType]

		for _, inp := range inputs {
			input, ok := inp.(map[string]interface{})
			if !ok {
				continue
			}

			inputName, _ := input["name"].(string)

			// Check required: first from revision data, then from action definition
			required, hasRequired := input["required"].(bool)
			if !hasRequired && requiredFromDef != nil {
				required = requiredFromDef[inputName]
			}

			if !required {
				continue
			}

			// Skip hidden inputs (visible_when condition not met)
			if vw, ok := input["visible_when"].(map[string]interface{}); ok {
				field, _ := vw["field"].(string)
				values, _ := vw["values"].([]interface{})
				if field != "" && len(values) > 0 {
					// Find the referenced field's value
					refValue := ""
					for _, inp2 := range inputs {
						if ref, ok := inp2.(map[string]interface{}); ok {
							if rn, _ := ref["name"].(string); rn == field {
								refValue, _ = ref["value"].(string)
								break
							}
						}
					}
					visible := false
					for _, v := range values {
						if vs, ok := v.(string); ok && vs == refValue {
							visible = true
							break
						}
					}
					if !visible {
						continue
					}
				}
			}

			value, _ := input["value"].(string)
			if strings.TrimSpace(value) == "" {
				return true
			}
		}
	}

	return false
}
