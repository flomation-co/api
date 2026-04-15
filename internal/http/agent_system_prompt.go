package http

// Agent system prompt assembly — Phase 1 of the Launch → API migration.
// Replaces 4-5 HTTP round-trips from Launch with a single endpoint that
// uses direct DB access.
//
// Route: POST /api/v1/internal/agent/:id/assemble-system-prompt

import (
	"net/http"

	"flomation.app/automate/api/internal/agent"
	apiconfig "flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/embedding"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type assembleSystemPromptRequest struct {
	ChannelType string `json:"channel_type"`
	AgentUserID string `json:"agent_user_id"`
	Content     string `json:"content"`
}

// assembleSystemPromptInternal handles POST /api/v1/internal/agent/:id/assemble-system-prompt.
// Builds the full system prompt with direct DB access — no HTTP round-trips.
func (s *Service) assembleSystemPromptInternal(c *gin.Context) {
	agentID := c.Param("id")

	var body assembleSystemPromptRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Fetch the agent to get the persona (system prompt).
	agentRecord, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agentRecord == nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to fetch agent for system prompt assembly")
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	persona := ""
	if agentRecord.SystemPrompt != nil {
		persona = *agentRecord.SystemPrompt
	}

	if s.promptAssembler == nil {
		// Fallback: return persona only
		c.JSON(http.StatusOK, agent.SystemPromptResult{
			Prompt: agent.BuildSystemPrompt(persona, nil, nil, nil, nil, body.ChannelType),
		})
		return
	}

	result := s.promptAssembler.AssembleSystemPrompt(agent.SystemPromptRequest{
		AgentID:     agentID,
		Persona:     persona,
		ChannelType: body.ChannelType,
		AgentUserID: body.AgentUserID,
		Content:     body.Content,
	})

	c.JSON(http.StatusOK, result)
}

// toolSummaryAdapter wraps the HTTP service to satisfy agent.ToolSummaryProvider.
type toolSummaryAdapter struct {
	svc *Service
}

func (a *toolSummaryAdapter) GetToolSummary(agentID string) ([]agent.AssembledTool, error) {
	agentRecord, err := a.svc.persistence.GetAgentByID(agentID)
	if err != nil || agentRecord == nil || agentRecord.OrchestratorFlowID == nil {
		return nil, err
	}

	revision, err := a.svc.persistence.GetLatestRevisionByFloID(*agentRecord.OrchestratorFlowID)
	if err != nil || revision == nil {
		return nil, err
	}

	// Reuse the same revision-parsing logic from the tool summary handler.
	tools := getToolsFromRevision(revision, a.svc.persistence)
	result := make([]agent.AssembledTool, 0, len(tools))
	for _, t := range tools {
		result = append(result, agent.AssembledTool{
			Type:        t.Type,
			Name:        t.Name,
			Description: t.Description,
		})
	}
	return result, nil
}

// initPromptAssembler creates the SystemPromptAssembler with optional
// embedding provider. Returns nil-safe — the assembler works without
// embeddings (degrades to pinned-only).
func (s *Service) initPromptAssembler(cfg *apiconfig.Config) *agent.SystemPromptAssembler {
	var emb embedding.Provider
	topK := 10

	if cfg.Embedding != nil && cfg.Embedding.Enabled {
		provider, err := embedding.NewBedrockProvider(
			cfg.Embedding.Region,
			cfg.Embedding.ModelID,
			cfg.Embedding.Dimensions,
			cfg.Embedding.AccessKeyID,
			cfg.Embedding.SecretAccessKey,
		)
		if err != nil {
			log.WithError(err).Warn("failed to initialise embedding provider — semantic search disabled")
		} else {
			emb = provider
			s.embeddingProvider = provider
			log.Info("embedding provider initialised for system prompt assembly")
		}
		if cfg.Embedding.TopK > 0 {
			topK = cfg.Embedding.TopK
		}
	}

	return agent.NewSystemPromptAssembler(
		s.persistence,
		&toolSummaryAdapter{svc: s},
		emb,
		topK,
	)
}
