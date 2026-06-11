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
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
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

	// All requests use the X-Flomation-Runner-Signature header.
	// POST: signature covers the request body.
	// GET:  signature covers the route :id parameter (no body).
	// Legacy: GET requests may also pass the signature as ?token= query param.
	header := c.GetHeader("X-Flomation-Runner-Signature")
	if header == "" {
		// Fall back to ?token= for backwards compatibility with older
		// executor binaries that sign environment GETs via query param.
		header = c.DefaultQuery("token", "")
	}
	if header == "" {
		return errors.New("missing signature (header or token param)")
	}

	sig, err := hex.DecodeString(header)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}

	var hash [32]byte
	if c.Request.Method == http.MethodGet {
		id := c.Param("id")
		if id == "" {
			return errors.New("unable to get execution id")
		}
		hash = sha256.Sum256([]byte(id))
	} else {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return err
		}
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
		hash = sha256.Sum256(body)
	}

	if err := rsa.VerifyPSS(key, crypto.SHA256, hash[:], sig, nil); err != nil {
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
		log.Error("execution is not assigned a runner, can't verify identity")
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

	if execution.TriggeredBy != nil {
		ti, err := s.persistence.GetTriggerInvocationById(*execution.TriggeredBy)
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to get trigger invocation")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		triggeredUser, err := s.persistence.GetUserByID(*ti.OwnerID)
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to get user by triggered ID")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		if triggeredUser == nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		c.Set("account_id", triggeredUser.ID)

		if ti.OrganisationID != nil {
			c.Set("organisation_id", *ti.OrganisationID)
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
	if !s.checkPermission(c, rbac.RunnerView) {
		return
	}

	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var allRunners []*api.Runner

	if len(user.Organisations) > 0 {
		orgID := user.Organisations[0].ID

		// Get runners from org queues
		queues, err := s.persistence.GetQueuesByOrganisationID(orgID)
		if err != nil {
			log.WithFields(log.Fields{"error": err}).Error("unable to get org queues")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		seen := make(map[string]bool)
		for _, q := range queues {
			qRunners, err := s.persistence.GetQueueRunners(q.ID)
			if err != nil {
				continue
			}
			for _, r := range qRunners {
				if !seen[r.ID] {
					seen[r.ID] = true
					allRunners = append(allRunners, r)
				}
			}
		}

		// Also include public runners if org allows it
		org, err := s.persistence.GetOrganisationByID(orgID)
		if err == nil && org != nil && org.AllowPublicRunners {
			publicRunners, err := s.persistence.GetRunners()
			if err == nil {
				for _, r := range publicRunners {
					if !seen[r.ID] {
						seen[r.ID] = true
						allRunners = append(allRunners, r)
					}
				}
			}
		}
	} else {
		// Personal mode — show all public runners
		runners, err := s.persistence.GetRunners()
		if err != nil {
			log.WithFields(log.Fields{"error": err}).Error("unable to get runners")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		allRunners = runners
	}

	if len(allRunners) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, allRunners)
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

		// Refresh version + executor_version on every re-registration so
		// the Runners page reflects what's actually running, not what
		// was running on first enrolment. Non-fatal: a transient DB blip
		// here shouldn't fail the registration call — heartbeat already
		// landed and the cosmetic stale version is the lesser evil.
		if err := s.persistence.UpdateRunnerVersion(r.ID, request.Version, request.ExecutorVersion); err != nil {
			log.WithFields(log.Fields{
				"error":     err,
				"runner_id": r.ID,
			}).Warn("unable to update runner version metadata")
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

	// Long-poll: if no execution found, wait for a notification or timeout
	if execution == nil {
		waitCh := s.executionNotifier.Wait("")
		select {
		case <-waitCh:
			// New execution available — re-query
			execution, err = s.persistence.GetExecutionForRunnerID(id)
			if err != nil || execution == nil {
				c.Status(http.StatusNoContent)
				return
			}
		case <-time.After(25 * time.Second):
			c.Status(http.StatusNoContent)
			return
		case <-c.Request.Context().Done():
			// Client disconnected
			return
		}
	}

	// Agent pause check: if the flow belongs to a paused agent, hold the
	// execution in the queue until the agent is resumed.
	if s.persistence.IsFlowAgentPaused(execution.FloID) {
		_ = s.persistence.UpdateExecutionStatus(execution.ID, "created")
		log.WithFields(log.Fields{
			"execution_id": execution.ID,
			"flo_id":       execution.FloID,
		}).Debug("execution returned to queue — agent is paused")
		c.Status(http.StatusNoContent)
		return
	}

	// Quota enforcement: if the flow's owner has exhausted their allowance
	// and has no credit, release the execution back to the queue.
	if blocked, _ := s.checkQuota(execution.FloID); blocked {
		_ = s.persistence.UpdateExecutionStatus(execution.ID, "created")
		log.WithFields(log.Fields{
			"execution_id": execution.ID,
			"flo_id":       execution.FloID,
		}).Debug("execution returned to queue — quota exhausted")
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

	// Enrich execution with author email
	if author, err := s.persistence.GetUserByID(execution.OwnerID); err == nil && author != nil {
		execution.AuthorEmail = author.EmailAddress
		// Default triggerer to author (overridden below if trigger invocation has a different owner)
		execution.TriggererEmail = author.EmailAddress
	}

	// Enrich execution.Data with the author's identities + user_id when
	// they're not already present. The inbound agent pipeline sets these
	// itself (using the message sender, not the flow author), so we
	// only fill them in for manual / scheduled runs where the executing
	// user IS the author. The runner picks these fields out of
	// triggerData and routes them onto ExecutionContext so ${flow.user_id}
	// and ${flow.identities} resolve in non-agent flows too.
	enrichDataWithAuthorIdentities(s.persistence, execution, flow)
	// Always runs AFTER the identity enrichment so it picks up whichever
	// user_id won (sender for inbound agent messages, author for manual /
	// scheduled runs). Adds ${user.X} substitution data.
	enrichDataWithUserVariables(s.persistence, execution)

	// Enrich with trigger type, triggerer email, and entry node ID from trigger invocation chain
	if execution.TriggeredBy != nil {
		if invocation, err := s.persistence.GetTriggerInvocationById(*execution.TriggeredBy); err == nil && invocation != nil {
			if trigger, err := s.persistence.GetTriggerByID(invocation.TriggerID); err == nil && trigger != nil {
				execution.TriggerType = &trigger.TypeName

				// Extract entry node ID from trigger data if available
				if trigger.Data != nil {
					var triggerData map[string]interface{}
					switch d := trigger.Data.(type) {
					case []byte:
						_ = json.Unmarshal(d, &triggerData)
					case map[string]interface{}:
						triggerData = d
					default:
						if raw, err := json.Marshal(d); err == nil {
							_ = json.Unmarshal(raw, &triggerData)
						}
					}
					if nodeID, ok := triggerData["__node_id"].(string); ok && nodeID != "" {
						execution.EntryNodeID = &nodeID
					}
				}
			}
			// If the invocation was triggered by a different user, look up their email
			if invocation.OwnerID != nil && *invocation.OwnerID != execution.OwnerID {
				if triggerer, err := s.persistence.GetUserByID(*invocation.OwnerID); err == nil && triggerer != nil {
					execution.TriggererEmail = triggerer.EmailAddress
				}
			}
		}
	}

	// Default trigger type to manual if not determined from invocation
	if execution.TriggerType == nil {
		manual := "manual"
		execution.TriggerType = &manual
	}

	// If entry node not yet determined, scan the flow revision for the first matching trigger node.
	// rev.Data has already been unmarshalled from []byte to map[string]interface{} above,
	// so we re-marshal and parse into a typed struct.
	if execution.EntryNodeID == nil && rev != nil && rev.Data != nil {
		var revNodes struct {
			Nodes []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
				Data struct {
					Label string `json:"label"`
				} `json:"data"`
			} `json:"nodes"`
		}
		if rawData, err := json.Marshal(rev.Data); err == nil {
			if err := json.Unmarshal(rawData, &revNodes); err == nil {
				triggerType := "trigger/manual"
				if execution.TriggerType != nil && *execution.TriggerType != "manual" {
					triggerType = "trigger/" + strings.ReplaceAll(*execution.TriggerType, "-", "_")
				}
				for _, n := range revNodes.Nodes {
					if n.Type == triggerType || n.Data.Label == triggerType {
						execution.EntryNodeID = &n.ID
						break
					}
				}
			}
		}
	}

	pe := api.PendingExecution{
		Flow:       *flow,
		Execution:  *execution,
		Data:       rev.Data,
		Checkpoint: execution.Checkpoint,
	}

	c.JSON(http.StatusOK, pe)
}

// enrichDataWithAuthorIdentities mutates execution.Data so that manual
// and scheduled executions carry the author's user_id and declared
// channel identities — the same fields the inbound agent pipeline sets
// for channel-triggered executions. The runner reads these out of
// triggerData and routes them onto ExecutionContext so the executor
// resolves ${flow.user_id} and ${flow.identities} for non-agent flows.
//
// Skips the enrichment when execution.Data already contains a user_id
// (the inbound agent pipeline ran first and set the channel sender —
// who isn't necessarily the flow author).
func enrichDataWithAuthorIdentities(p Persistence, execution *api.Execution, flow *api.Flo) {
	if execution == nil {
		return
	}

	// Unmarshal current execution.Data into a generic map (preserve any
	// existing trigger fields the runner relies on).
	data := map[string]interface{}{}
	if len(execution.Data) > 0 && string(execution.Data) != "null" {
		_ = json.Unmarshal(execution.Data, &data)
	}

	// Inbound agent pipeline wins — don't overwrite its sender resolution.
	if _, hasUser := data["user_id"]; hasUser {
		return
	}
	if execution.OwnerID == "" {
		return
	}

	data["user_id"] = execution.OwnerID

	// Identity snapshot is scoped to the flow's organisation context,
	// matching the inbound agent pipeline's GetUserIdentitiesByUserAndOrg
	// call. A nil OrganisationID means personal mode — IS NOT DISTINCT FROM
	// NULL inside the persistence layer matches the right rows.
	var orgID *string
	if flow != nil && flow.OrganisationID != nil && *flow.OrganisationID != "" {
		orgID = flow.OrganisationID
	}

	identities, err := p.GetUserIdentitiesByUserAndOrg(execution.OwnerID, orgID)
	if err == nil && len(identities) > 0 {
		shaped := make([]map[string]interface{}, 0, len(identities))
		for _, i := range identities {
			if i == nil {
				continue
			}
			row := map[string]interface{}{
				"channel_type": i.ChannelType,
				"external_id":  i.ExternalID,
			}
			if i.DisplayName != nil && *i.DisplayName != "" {
				row["display_name"] = *i.DisplayName
			}
			shaped = append(shaped, row)
		}
		data["identities"] = shaped
	}

	if raw, err := json.Marshal(data); err == nil {
		execution.Data = raw
	}
}

// enrichDataWithUserVariables mutates execution.Data to inject the
// executing user's profile as a "user_variables" map. The runner forwards
// it onto ExecutionContext.UserVariables so the executor resolves
// ${user.first_name}, ${user.full_address}, etc. Pre-composed full_name
// and full_address use the same canonical formatting as the editor
// preview (composeFullName / composeFullAddress in profile.go) so flow
// authors never see a stale shape.
//
// Reads data["user_id"] — whichever path wrote it (inbound agent sender
// or manual/scheduled author). No-ops cleanly when user_id is absent
// (e.g. anonymous webhook trigger).
func enrichDataWithUserVariables(p Persistence, execution *api.Execution) {
	if execution == nil {
		return
	}

	data := map[string]interface{}{}
	if len(execution.Data) > 0 && string(execution.Data) != "null" {
		_ = json.Unmarshal(execution.Data, &data)
	}

	userID, _ := data["user_id"].(string)
	if userID == "" {
		return
	}

	user, err := p.GetUserByID(userID)
	if err != nil || user == nil {
		return
	}

	vars := map[string]string{
		"id":             user.ID,
		"name":           user.Name,
		"salutation":     deref(user.Salutation),
		"first_name":     deref(user.FirstName),
		"last_name":      deref(user.LastName),
		"job_title":      deref(user.JobTitle),
		"address_line_1": deref(user.AddressLine1),
		"address_line_2": deref(user.AddressLine2),
		"city":           deref(user.City),
		"region":         deref(user.Region),
		"postcode":       deref(user.Postcode),
		"country":        deref(user.Country),
	}
	if user.EmailAddress != nil {
		vars["email"] = *user.EmailAddress
	}
	vars["full_name"] = composeFullName(vars["salutation"], vars["first_name"], vars["last_name"], user.Name)
	vars["full_address"] = composeFullAddress(vars["address_line_1"], vars["address_line_2"],
		vars["city"], vars["region"], vars["postcode"], vars["country"])

	data["user_variables"] = vars

	if raw, err := json.Marshal(data); err == nil {
		execution.Data = raw
	}
}
