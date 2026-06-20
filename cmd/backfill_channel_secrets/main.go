// Package main is a one-shot data migration: for every named agent that
// still has credentials in agent.channels (JSONB), it creates environment
// secrets for each credential value, writes a NEW orchestrator-flow
// revision whose channel trigger node inputs reference ${secrets.X}, and
// upserts the corresponding trigger record so Launch picks the new data
// up immediately.
//
// SAFETY: defaults to dry-run. Passes --commit to mutate. Accepts agent
// IDs as positional arguments — this is deliberate: we do NOT iterate
// every agent in the database, the operator picks them. This bounds
// blast radius for a job that touches production flow revisions and
// secret material.
//
// Idempotency: secrets are created via GetEnvironmentSecretByName
// pre-check; if the deterministic name already exists, we reuse it.
// Revisions are append-only by design, so re-runs will create
// additional revisions but the resulting flow state converges.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

func main() {
	var (
		configPath = flag.String("config", "config.json", "Path to API config.json")
		commit     = flag.Bool("commit", false, "Actually mutate state. Default is dry-run.")
		verbose    = flag.Bool("verbose", false, "Verbose logging")
	)
	flag.Parse()

	if *verbose {
		log.SetLevel(log.DebugLevel)
	}

	agentIDs := flag.Args()
	if len(agentIDs) == 0 {
		log.Error("no agent IDs supplied — pass one or more agent UUIDs as positional arguments")
		os.Exit(2)
	}

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.WithError(err).Fatal("unable to load config")
	}

	db, err := persistence.NewService(cfg)
	if err != nil {
		log.WithError(err).Fatal("unable to create persistence service")
	}

	mode := "DRY-RUN"
	if *commit {
		mode = "COMMIT"
	}
	log.WithFields(log.Fields{"mode": mode, "agents": len(agentIDs)}).Info("starting channel-secret backfill")

	total := 0
	for _, agentID := range agentIDs {
		summary, err := backfillAgent(db, agentID, *commit)
		if err != nil {
			log.WithError(err).WithField("agent_id", agentID).Error("backfill failed for agent")
			continue
		}
		log.WithFields(log.Fields{
			"agent_id":         agentID,
			"channels_found":   summary.ChannelsFound,
			"secrets_created":  summary.SecretsCreated,
			"secrets_existing": summary.SecretsExisting,
			"nodes_patched":    summary.NodesPatched,
			"orphan_created":   summary.OrphansCreated,
		}).Info("agent processed")
		total++
	}
	log.WithField("count", total).Info("backfill complete")
}

// Summary captures per-agent results for the audit log.
type Summary struct {
	ChannelsFound   int
	SecretsCreated  int
	SecretsExisting int
	NodesPatched    int
	OrphansCreated  int
}

func backfillAgent(db *persistence.Service, agentID string, commit bool) (Summary, error) {
	var sum Summary

	agent, err := db.GetAgentByID(agentID)
	if err != nil {
		return sum, fmt.Errorf("get agent: %w", err)
	}
	if agent == nil {
		return sum, errors.New("agent not found")
	}
	if agent.OrchestratorFlowID == nil || *agent.OrchestratorFlowID == "" {
		return sum, errors.New("agent has no orchestrator flow — channel triggers can't be configured")
	}

	flo, err := db.GetFloByID(*agent.OrchestratorFlowID)
	if err != nil {
		return sum, fmt.Errorf("get flow: %w", err)
	}
	if flo.EnvironmentID == nil || *flo.EnvironmentID == "" {
		return sum, errors.New("orchestrator flow has no environment — cannot store secrets")
	}

	env, err := db.GetEnvironmentByIDDirect(*flo.EnvironmentID)
	if err != nil {
		return sum, fmt.Errorf("get environment: %w", err)
	}

	channels, err := parseChannels(agent.Channels)
	if err != nil {
		return sum, fmt.Errorf("parse channels: %w", err)
	}
	sum.ChannelsFound = len(channels)
	if len(channels) == 0 {
		log.WithField("agent_id", agentID).Info("agent has no channels — nothing to backfill")
		return sum, nil
	}

	rev, err := db.GetLatestRevisionByFloID(*agent.OrchestratorFlowID)
	if err != nil {
		return sum, fmt.Errorf("get latest revision: %w", err)
	}

	revisionJSON, err := normaliseRevisionData(rev.Data)
	if err != nil {
		return sum, fmt.Errorf("normalise revision data: %w", err)
	}

	// secretPlan: name → plaintext value; will be created in the env.
	// nodeUpdates: channelType → input name → ${secrets.X} reference.
	secretPlan := map[string]string{}
	nodeUpdates := map[string]map[string]string{}

	agentSlug := agentID[:8]

	for _, ch := range channels {
		updates := map[string]string{}
		for credKey, rawVal := range ch.Config {
			strVal, ok := rawVal.(string)
			if !ok || strVal == "" {
				continue // skip non-string or empty creds (e.g. allowed_chat_ids arrays)
			}
			secretName := fmt.Sprintf("%s_%s_%s", ch.Type, credKey, agentSlug)
			secretPlan[secretName] = strVal
			updates[credKey] = "${secrets." + secretName + "}"
		}
		if len(updates) > 0 {
			nodeUpdates[ch.Type] = updates
		}
	}

	// Reconcile secrets. Idempotent: any secret whose name already exists
	// in the env is left alone (we trust prior backfills).
	for name, value := range secretPlan {
		existing, err := db.GetEnvironmentSecretByName(env.ID, env.SecretKey, name)
		if err != nil {
			log.WithError(err).WithField("name", name).Warn("error checking existing secret — will attempt create")
		}
		if existing != nil {
			sum.SecretsExisting++
			log.WithField("name", name).Debug("secret already exists, skipping")
			continue
		}
		log.WithFields(log.Fields{"name": name, "len": len(value)}).Info("planning secret create")
		if !commit {
			sum.SecretsCreated++ // dry-run: count what we WOULD create
			continue
		}
		_, err = db.CreateEnvironmentSecret(env.ID, env.SecretKey, api.CreateEnvironmentSecret{
			EnvironmentID: env.ID,
			Name:          name,
			Value:         value,
			Provider:      "KeyValue",
		})
		if err != nil {
			return sum, fmt.Errorf("create secret %q: %w", name, err)
		}
		sum.SecretsCreated++
	}

	// Patch revision JSON: walk nodes, find trigger/<type> matches, update input values.
	// Treats nodes whose data.label starts with "trigger/" as the target set.
	patched, patchCount, orphans := patchTriggerNodes(revisionJSON, nodeUpdates)
	sum.NodesPatched = patchCount
	sum.OrphansCreated = orphans

	if patchCount == 0 && len(nodeUpdates) > 0 {
		log.WithFields(log.Fields{
			"agent_id":            agentID,
			"channels_with_creds": len(nodeUpdates),
		}).Warn("no matching trigger nodes found in orchestrator flow — credentials minted but flow not updated; user must add the trigger nodes manually")
	}

	patchedRaw, err := json.Marshal(patched)
	if err != nil {
		return sum, fmt.Errorf("marshal patched revision: %w", err)
	}

	if !commit {
		log.WithFields(log.Fields{"agent_id": agentID, "would_patch_nodes": patchCount}).Info("DRY-RUN: would write new revision")
		return sum, nil
	}

	newRev := api.Revision{
		FloID: *agent.OrchestratorFlowID,
		Data:  json.RawMessage(patchedRaw),
	}
	newRevID, err := db.CreateFloRevision(newRev)
	if err != nil {
		return sum, fmt.Errorf("create revision: %w", err)
	}
	log.WithFields(log.Fields{"agent_id": agentID, "revision_id": *newRevID}).Info("wrote new revision")

	// Push the new resolved-references trigger data so Launch picks up the
	// signing_secret / bot_token on the next webhook. The full per-flow
	// trigger sync (matching node-id, etc.) is handled by createFloRevision
	// in the HTTP layer when a user saves a revision; in this one-shot we
	// update existing triggers in-place.
	if err := syncTriggers(db, *agent.OrchestratorFlowID, patched); err != nil {
		log.WithError(err).Warn("trigger sync after revision write failed — Launch may not pick up new creds until the agent restarts or the flow is re-saved in the editor")
	}

	return sum, nil
}

// channelEntry is the unmarshalled shape of one element in agent.channels.
type channelEntry struct {
	Type   string                 `json:"type"`
	Config map[string]interface{} `json:"config"`
}

func parseChannels(raw json.RawMessage) ([]channelEntry, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var out []channelEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// normaliseRevisionData converts whatever shape persistence handed us (it
// can be []byte, json.RawMessage, string, or map) into a generic map for
// in-place mutation.
func normaliseRevisionData(in interface{}) (map[string]interface{}, error) {
	var raw []byte
	switch v := in.(type) {
	case []byte:
		raw = v
	case json.RawMessage:
		raw = v
	case string:
		raw = []byte(v)
	case map[string]interface{}:
		return v, nil
	default:
		// Best-effort: marshal back to JSON then re-parse.
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// patchTriggerNodes walks the nodes array and rewrites matching trigger
// nodes' input values. Returns the patched map (same instance, mutated),
// the count of nodes patched, and the count of orphans created. We do NOT
// create orphan nodes in this implementation — the caller is told the
// agent has no matching trigger and an operator follow-up is required.
func patchTriggerNodes(rev map[string]interface{}, updates map[string]map[string]string) (map[string]interface{}, int, int) {
	nodes, _ := rev["nodes"].([]interface{})
	patched := 0

	for _, n := range nodes {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		data, _ := node["data"].(map[string]interface{})
		if data == nil {
			continue
		}
		label, _ := data["label"].(string)
		if !strings.HasPrefix(label, "trigger/") {
			continue
		}
		channelType := strings.TrimPrefix(label, "trigger/")
		updateMap, has := updates[channelType]
		if !has {
			continue
		}

		cfg, _ := data["config"].(map[string]interface{})
		if cfg == nil {
			cfg = map[string]interface{}{}
			data["config"] = cfg
		}
		inputs, _ := cfg["inputs"].([]interface{})
		if inputs == nil {
			inputs = []interface{}{}
		}

		// Replace or append each updated input.
		for name, ref := range updateMap {
			found := false
			for _, ip := range inputs {
				ipMap, ok := ip.(map[string]interface{})
				if !ok {
					continue
				}
				if n, _ := ipMap["name"].(string); n == name {
					ipMap["value"] = ref
					found = true
					break
				}
			}
			if !found {
				inputs = append(inputs, map[string]interface{}{
					"name":  name,
					"value": ref,
				})
			}
		}
		cfg["inputs"] = inputs
		patched++
	}

	return rev, patched, 0
}

// syncTriggers re-derives the trigger data for each trigger/* node in the
// new revision and writes it into the existing trigger record(s) via
// UpdateTrigger. This mirrors what createFloRevision's HTTP handler does
// when a revision is saved through the editor.
func syncTriggers(db *persistence.Service, floID string, rev map[string]interface{}) error {
	triggers, err := db.GetTriggersByFloID(floID)
	if err != nil {
		return err
	}
	byType := map[string]*api.Trigger{}
	for _, t := range triggers {
		t := t
		byType[t.TypeName] = t
	}

	nodes, _ := rev["nodes"].([]interface{})
	for _, n := range nodes {
		node, _ := n.(map[string]interface{})
		if node == nil {
			continue
		}
		data, _ := node["data"].(map[string]interface{})
		if data == nil {
			continue
		}
		label, _ := data["label"].(string)
		if !strings.HasPrefix(label, "trigger/") {
			continue
		}
		typeName := strings.ReplaceAll(strings.TrimPrefix(label, "trigger/"), "_", "-")
		existing, ok := byType[typeName]
		if !ok {
			continue // trigger not yet registered — full sync needs HTTP path
		}

		nodeID, _ := node["id"].(string)
		cfg, _ := data["config"].(map[string]interface{})
		inputs, _ := cfg["inputs"].([]interface{})

		td := map[string]interface{}{"__node_id": nodeID}
		for _, ip := range inputs {
			ipMap, _ := ip.(map[string]interface{})
			if ipMap == nil {
				continue
			}
			name, _ := ipMap["name"].(string)
			if name == "" {
				continue
			}
			td[name] = ipMap["value"]
		}

		updated := api.Trigger{
			ID:       existing.ID,
			Name:     label,
			TypeName: typeName,
			FloID:    &floID,
			Data:     td,
		}
		if err := db.UpdateTrigger(updated); err != nil {
			return fmt.Errorf("update trigger %s: %w", existing.ID, err)
		}
	}
	return nil
}
