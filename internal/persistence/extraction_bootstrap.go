package persistence

// Phase 2d-γ: Go bootstrap helper that seeds the canonical extraction
// System Flow at API startup if it doesn't already exist. See
// plans/agent_memory.md §"The extraction pipeline" and §"Phase 2" for
// the full design rationale.
//
// Why Go bootstrap instead of SQL migration:
//   - Flow JSON references node IDs, edge IDs, and action input wiring
//     that must stay in sync with the executor's action type names and
//     the editor's connection schema. A Go helper keeps the definition
//     close to the types rather than buried in an opaque SQL INSERT.
//   - The helper is idempotent: runs on every startup, checks for an
//     existing extraction flow, and only creates one if none exists.
//     If the flow is accidentally deleted or the DB is migrated to a
//     fresh instance, the next API restart recreates it.
//   - Admins who customise the extraction prompt can do so by editing
//     the flow's revision via the editor once the "show system flows"
//     admin toggle lands in Phase 6. The bootstrap only creates the
//     initial copy; it does not overwrite subsequent revisions.

import (
	"encoding/json"
	"fmt"

	"flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// ExtractionFlowPurpose is the system_flow_purpose value for the
// canonical extraction pipeline flow. Used in lookups and as the
// value the bootstrap writes to the flo row.
const ExtractionFlowPurpose = "agent_extraction"

// extractionSystemPrompt is the structured-output prompt fed to the
// Anthropic Haiku node in the extraction flow. It asks the model to
// return a JSON object with four arrays: memories, proposed_actions,
// commitments, confirmations.
//
// The prompt includes:
//   - A schema description with all valid types and fields.
//   - Explicit confidence-score guidance (0.0–1.0, higher = more certain).
//   - Two few-shot examples so Haiku has concrete anchors for the
//     output shape. Haiku's compliance on structured JSON goes from
//     ~70% bare-schema to ~95%+ with examples — the ~300 extra tokens
//     are cheap insurance.
//   - An instruction to return an empty object (`{}`) when there's
//     nothing to extract, rather than hallucinating filler.
const extractionSystemPrompt = `You are a memory extraction engine for an AI assistant platform. Your job is to analyse a single conversational turn and extract structured information.

You will receive a message from either the user ("role": "user") or the assistant ("role": "assistant"). Analyse it and return a JSON object with these arrays:

## Output schema

{
  "memories": [
    {
      "type": "preference|feedback|fact|relationship|task|session_summary",
      "title": "Short handle (3-8 words)",
      "body": "The fact itself, written as a statement about the user",
      "confidence": 0.0-1.0
    }
  ],
  "proposed_actions": [
    {
      "type": "identity_link|forget_memory|correct_memory",
      "evidence": "The exact user utterance that triggered this",
      "confidence": 0.0-1.0,
      "payload": {}
    }
  ],
  "commitments": [
    {
      "kind": "followup|reminder|monitor|chase",
      "description": "What was promised",
      "trigger_type": "time_elapsed|absolute_time|condition|user_prompt",
      "due_in": "human-readable duration if applicable",
      "due_at": "ISO-8601 if a specific time was mentioned",
      "evidence": "The exact utterance containing the promise",
      "confidence": 0.0-1.0,
      "made_by": "user|assistant"
    }
  ],
  "confirmations": [
    {
      "pending_action_id": "UUID if known",
      "resolution": "confirmed|declined",
      "evidence": "The exact utterance"
    }
  ]
}

## Memory types

- preference: user preferences ("call me Andy not Andrew", "I prefer bullet points")
- feedback: guidance on assistant behaviour ("don't be verbose", "always show code")
- fact: factual information about the user ("lives in London", "works at Flomation")
- relationship: people the user mentions ("Dave is their co-founder")
- task: active tasks or obligations ("owes Sarah a response on Q3 roadmap")
- session_summary: only used by the platform for session summaries, never by extraction

## Confidence scores

- 1.0: explicit, unambiguous statement ("My name is Andy")
- 0.8-0.95: strong inference from context ("I'll be in the London office" → lives near London)
- 0.5-0.8: reasonable but uncertain ("I think we discussed this last week")
- Below 0.5: too uncertain to store — omit rather than include

## Few-shot examples

Input: {"role": "user", "content": "Hey, I'm Andy. I work in Go mostly but I'm learning React for this project."}
Output:
{
  "memories": [
    {"type": "preference", "title": "Preferred name", "body": "Prefers to be called Andy", "confidence": 0.95},
    {"type": "fact", "title": "Primary language", "body": "Works primarily in Go", "confidence": 0.9},
    {"type": "fact", "title": "Learning React", "body": "Currently learning React for their project", "confidence": 0.85}
  ],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": []
}

Input: {"role": "assistant", "content": "I'll compile a shortlist of options and get back to you within the hour."}
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [
    {"kind": "followup", "description": "Compile a shortlist of options and report back", "trigger_type": "time_elapsed", "due_in": "1 hour", "evidence": "I'll compile a shortlist of options and get back to you within the hour.", "confidence": 0.9, "made_by": "assistant"}
  ],
  "confirmations": []
}

## Rules

- Return ONLY valid JSON. No markdown fences, no commentary.
- If there is nothing to extract, return: {"memories":[],"proposed_actions":[],"commitments":[],"confirmations":[]}
- Never invent information that is not in the message.
- Preference and feedback memories should have high confidence (0.85+) since they are auto-pinned.
- Commitments from assistant turns should set "made_by": "assistant"; from user turns, "made_by": "user".
- For identity_link proposed_actions, include the claimed channel and handle in the payload.`

// BootstrapExtractionFlow ensures the canonical extraction System Flow
// exists. It is idempotent: re-running after the flow already exists is
// a no-op. Called once from main() after migrations and before the HTTP
// server starts serving.
//
// Steps:
//  1. Check if a flo with system_flow_purpose='agent_extraction' exists.
//  2. If not, create it via the normal CreateFlo path (which auto-links
//     a manual trigger), mark it as system_flow=TRUE, and create an
//     initial revision with the 3-node extraction flow structure.
//  3. Backfill: update all agents with extraction_flow_id=NULL to point
//     at the new flow. New agents created via CreateAgent after this
//     point will also get the flow ID via a DEFAULT or application-level
//     logic (handled separately in Phase 2d-γ follow-up or editor).
func (s *Service) BootstrapExtractionFlow() error {
	// Step 1: check for existing flow.
	var existingID string
	err := s.conn.Get(&existingID,
		`SELECT id FROM flo WHERE system_flow = TRUE AND system_flow_purpose = $1 LIMIT 1`,
		ExtractionFlowPurpose,
	)
	if err == nil && existingID != "" {
		log.WithFields(log.Fields{
			"flow_id": existingID,
		}).Info("extraction system flow already exists, skipping bootstrap")

		// Still backfill agents that were created since last restart
		// and might have NULL extraction_flow_id.
		return s.backfillExtractionFlowID(existingID)
	}

	// Step 2: create the flow.
	log.Info("creating canonical extraction system flow")

	floName := "Agent Memory Extraction"
	floID, err := s.CreateFlo(Flo{
		Name: floName,
	})
	if err != nil {
		return fmt.Errorf("failed to create extraction flow: %w", err)
	}

	// Mark as system flow (these columns aren't on the Go struct, so
	// we use raw SQL). Also set the name again in case CreateFlo
	// normalises it.
	if _, err := s.conn.Exec(
		`UPDATE flo SET system_flow = TRUE, system_flow_purpose = $1, name = $2 WHERE id = $3`,
		ExtractionFlowPurpose, floName, *floID,
	); err != nil {
		return fmt.Errorf("failed to mark extraction flow as system: %w", err)
	}

	// Build the 3-node flow revision (trigger → ai/anthropic → process_extraction).
	revisionData := buildExtractionFlowJSON()
	revisionDataBytes, err := json.Marshal(revisionData)
	if err != nil {
		return fmt.Errorf("failed to marshal extraction flow revision: %w", err)
	}

	if _, err := s.CreateFloRevision(Revision{
		FloID: *floID,
		Data:  json.RawMessage(revisionDataBytes),
	}); err != nil {
		return fmt.Errorf("failed to create extraction flow revision: %w", err)
	}

	log.WithFields(log.Fields{
		"flow_id": *floID,
	}).Info("extraction system flow created")

	// Step 3: backfill all agents.
	return s.backfillExtractionFlowID(*floID)
}

// backfillExtractionFlowID sets extraction_flow_id for all agents that
// don't have one yet. Returns the number of rows updated.
func (s *Service) backfillExtractionFlowID(flowID string) error {
	result, err := s.conn.Exec(
		`UPDATE agent SET extraction_flow_id = $1 WHERE extraction_flow_id IS NULL AND archived_at IS NULL`,
		flowID,
	)
	if err != nil {
		return fmt.Errorf("failed to backfill extraction_flow_id: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		log.WithFields(log.Fields{
			"flow_id":        flowID,
			"agents_updated": rows,
		}).Info("backfilled extraction_flow_id for existing agents")
	}
	return nil
}

// buildExtractionFlowJSON constructs the 3-node flow structure. Node
// IDs are deterministic so that edges can reference them and so that
// re-running the bootstrap on a fresh DB produces the same flow shape.
func buildExtractionFlowJSON() map[string]interface{} {
	triggerNodeID := "extraction-trigger-001"
	anthropicNodeID := "extraction-anthropic-002"
	processNodeID := "extraction-process-003"

	return map[string]interface{}{
		"nodes": []map[string]interface{}{
			{
				"id":   triggerNodeID,
				"type": "trigger",
				"data": map[string]interface{}{
					"label":  "manual",
					"name":   "Extraction Trigger",
					"icon":   "play",
					"inputs": []interface{}{},
				},
				"position": map[string]interface{}{"x": 250, "y": 50},
			},
			{
				"id":   anthropicNodeID,
				"type": "action",
				"data": map[string]interface{}{
					"label": "ai/anthropic",
					"name":  "Extract Memories",
					"icon":  "brain",
					"inputs": []map[string]interface{}{
						{"name": "model", "value": "claude-haiku-4-5-20251001"},
						{"name": "system_prompt", "value": extractionSystemPrompt},
						{"name": "user_prompt", "value": "${trigger.content}"},
						{"name": "max_tokens", "value": "2048"},
						{"name": "temperature", "value": "0"},
					},
				},
				"position": map[string]interface{}{"x": 250, "y": 200},
			},
			{
				"id":   processNodeID,
				"type": "action",
				"data": map[string]interface{}{
					"label": "agent/process_extraction",
					"name":  "Process Extraction Results",
					"icon":  "brain",
					"inputs": []map[string]interface{}{
						{"name": "agent_id", "value": "${trigger.agent_id}"},
						{"name": "extraction_json", "value": fmt.Sprintf("${node.%s.response}", anthropicNodeID)},
						{"name": "agent_user_id", "value": "${trigger.agent_user_id}"},
						{"name": "conversation_id", "value": "${trigger.conversation_id}"},
						{"name": "source_message_id", "value": "${trigger.message_id}"},
					},
				},
				"position": map[string]interface{}{"x": 250, "y": 400},
			},
		},
		"edges": []map[string]interface{}{
			{
				"id":     "extraction-edge-001",
				"source": triggerNodeID,
				"target": anthropicNodeID,
			},
			{
				"id":     "extraction-edge-002",
				"source": anthropicNodeID,
				"target": processNodeID,
			},
		},
	}
}

// Flo is a local type alias to avoid importing the api package from
// persistence (which would create a circular dependency in tests). The
// real api.Flo is used at the call site in main.go; this alias lets
// the bootstrap helper create a flow via CreateFlo without the import.
// The db tags match the api.Flo struct exactly.
type Flo = api.Flo

// Revision is a local type alias matching api.Revision.
type Revision = api.Revision
