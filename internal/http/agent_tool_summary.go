package http

// Agent tool summary — returns a list of tool nodes in an agent's
// orchestrator flow. Used by Launch to dynamically generate the
// tools directive in the system prompt.
//
// Route: GET /api/v1/internal/agent/:id/tool-summary

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type toolSummaryEntry struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// getAgentToolSummaryInternal handles GET /api/v1/internal/agent/:id/tool-summary.
// Parses the latest revision of the agent's orchestrator flow and returns
// a deduplicated list of tool nodes (excluding triggers and AI nodes).
func (s *Service) getAgentToolSummaryInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if agent.OrchestratorFlowID == nil || *agent.OrchestratorFlowID == "" {
		c.JSON(http.StatusOK, []toolSummaryEntry{})
		return
	}

	// Fetch the latest revision.
	revision, err := s.persistence.GetLatestRevisionByFloID(*agent.OrchestratorFlowID)
	if err != nil || revision == nil {
		c.JSON(http.StatusOK, []toolSummaryEntry{})
		return
	}

	// Parse the revision data to extract nodes.
	var flowData struct {
		Nodes []struct {
			Type string `json:"type"`
			Data struct {
				Label  string `json:"label"`
				Config struct {
					Name string `json:"name"`
				} `json:"config"`
			} `json:"data"`
		} `json:"nodes"`
	}

	// revision.Data is interface{} — it may be a string (JSON text from
	// the DB), []byte, or an already-parsed map. Handle all cases.
	var rawData []byte
	switch v := revision.Data.(type) {
	case string:
		rawData = []byte(v)
	case []byte:
		rawData = v
	default:
		var err error
		rawData, err = json.Marshal(v)
		if err != nil {
			log.WithError(err).Error("failed to marshal orchestrator flow revision data")
			c.JSON(http.StatusOK, []toolSummaryEntry{})
			return
		}
	}

	if err := json.Unmarshal(rawData, &flowData); err != nil {
		log.WithError(err).Error("failed to parse orchestrator flow revision")
		c.JSON(http.StatusOK, []toolSummaryEntry{})
		return
	}

	// Extract tool nodes: skip triggers (type 1), AI nodes, and internal
	// nodes like set_variable. Deduplicate by type.
	seen := make(map[string]bool)
	var tools []toolSummaryEntry

	// Look up descriptions from the actions table (plugin field = node type).
	actionDescs := make(map[string]string)
	actionNames := make(map[string]string)
	if actions, err := s.persistence.GetActions(); err == nil {
		for _, a := range actions {
			actionDescs[a.Plugin] = a.Description
			actionNames[a.Plugin] = a.Name
		}
	}

	for _, node := range flowData.Nodes {
		nodeType := node.Type

		// Skip triggers, AI actions, and utility nodes
		if isInternalNode(nodeType) {
			continue
		}

		if seen[nodeType] {
			continue
		}
		seen[nodeType] = true

		name := node.Data.Config.Name
		if name == "" {
			if n, ok := actionNames[nodeType]; ok {
				name = n
			} else {
				name = node.Data.Label
			}
		}

		desc := ""
		if d, ok := actionDescs[nodeType]; ok {
			desc = d
		}

		tools = append(tools, toolSummaryEntry{
			Type:        nodeType,
			Name:        name,
			Description: desc,
		})
	}

	c.JSON(http.StatusOK, tools)
}

// isInternalNode returns true for node types that shouldn't appear
// in the tools directive.
func isInternalNode(nodeType string) bool {
	switch {
	case len(nodeType) > 8 && nodeType[:8] == "trigger/":
		return true
	case len(nodeType) > 3 && nodeType[:3] == "ai/":
		return true
	case nodeType == "common/set_variable":
		return true
	case nodeType == "conditional/switch":
		return true
	case nodeType == "conditional/if":
		return true
	case nodeType == "conditional/for":
		return true
	case nodeType == "conditional/while":
		return true
	case nodeType == "output/set":
		return true
	case nodeType == "output/set_outputs":
		return true
	case nodeType == "error/on_error":
		return true
	case nodeType == "common/sleep":
		return true
	}
	return false
}