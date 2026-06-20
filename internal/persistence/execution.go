package persistence

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"

	api "flomation.app/automate/api"
)

// MaxExecutionDepth caps how deep a hierarchical execution tree can
// grow. The hierarchy machinery (see migration 93) stores depth as a
// cached integer so root-only queries and breadcrumb walks stay O(1)
// per row. The cap keeps the recursive ancestors CTE bounded and
// prevents runaway loops where a flow keeps spawning children.
const MaxExecutionDepth = 10

// ParentLink describes how a newly-created execution links back to
// the one that spawned it. The relationship classifier is opaque to
// the persistence layer — callers pick values like "plan_task",
// "remote_trigger" or "subflow" that the UI can render. Metadata
// carries free-form context (plan_id, originating_flow_id, etc.).
//
// Metadata is optional; nil is rendered as SQL NULL in the
// parent_metadata column.
type ParentLink struct {
	ExecutionID  string
	Relationship string
	Metadata     json.RawMessage
}

// ErrParentExecutionNotFound is returned by TriggerExecution when a
// caller supplies a ParentLink whose ExecutionID does not exist. We
// surface this as a distinct error so the webhook handler can map it
// to a 400 — a missing parent is a caller bug, not a server fault.
var ErrParentExecutionNotFound = errors.New("parent execution not found")

// resolveParent looks up the parent row in the same transaction the
// child is about to be inserted in and returns the root id + the
// child's intended depth. The depth is the parent's depth + 1,
// clamped to MaxExecutionDepth.
//
// The bool return value reports whether clamping kicked in so the
// caller can flag the row's parent_metadata with `depth_capped: true`
// — this keeps the policy decision out of the SQL and lets us audit
// trees that hit the ceiling.
func (s *Service) resolveParent(tx *sqlx.Tx, parentID string) (rootID string, depth int, capped bool, err error) {
	var row struct {
		RootExecutionID string `db:"root_execution_id"`
		Depth           int    `db:"depth"`
	}

	if err := tx.Get(&row, `
		SELECT root_execution_id, depth
		FROM execution
		WHERE id = $1
	`, parentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, false, fmt.Errorf("%w: %s", ErrParentExecutionNotFound, parentID)
		}
		return "", 0, false, err
	}

	depth = row.Depth + 1
	if depth > MaxExecutionDepth {
		depth = MaxExecutionDepth
		capped = true
	}
	return row.RootExecutionID, depth, capped, nil
}

// treeRowColumns is the column list every hierarchy query returns.
// Keeping it in one place ensures GetExecutionTree, GetExecutionAncestors,
// and GetExecutionDirectChildren stay schema-aligned with the row
// shape the editor renders.
const treeRowColumns = `e.id, e.flo_id, f.name, e.owner_id, e.organisation_id,
	e.created_at, e.updated_at, e.completed_at, e.triggered_by,
	e.execution_status, e.completion_status,
	e.result->'duration' AS duration, e.result->'billingDuration' AS billing_duration,
	tt.name AS trigger_type,
	e.agent_id,
	e.parent_execution_id, e.parent_relationship, e.parent_metadata,
	e.root_execution_id, e.depth,
	EXISTS (SELECT 1 FROM execution c WHERE c.parent_execution_id = e.id LIMIT 1) AS has_children`

const treeRowJoins = `INNER JOIN flo f ON f.id = e.flo_id
	LEFT JOIN trigger_invocation ti ON ti.id = e.triggered_by
	LEFT JOIN trigger t ON t.id = ti.trigger_id
	LEFT JOIN trigger_type tt ON tt.id = t.type`

// GetExecutionTree returns every execution in the tree rooted at
// rootID, ordered by depth ascending and created_at ascending so the
// editor can render rows in natural top-down order. The hot path is
// the execution_root_created_idx index from migration 93.
//
// The caller is responsible for visibility — verify the requesting
// user can see at least the root row before exposing the tree.
func (s *Service) GetExecutionTree(rootID string) ([]*api.Execution, error) {
	var rows []*api.Execution
	q := `SELECT ` + treeRowColumns + `,
		(SELECT COUNT(*) FROM execution e2
			WHERE e2.flo_id = e.flo_id AND e2.created_at <= e.created_at) AS sequence
		FROM execution e
		` + treeRowJoins + `
		WHERE e.root_execution_id = $1
		ORDER BY e.depth ASC, e.created_at ASC`
	if err := s.conn.Select(&rows, q, rootID); err != nil {
		return nil, err
	}
	return rows, nil
}

// GetExecutionAncestors walks parent_execution_id upward from the
// given execution and returns the chain root-first (excluding the
// execution itself). The recursive CTE is bounded by MaxExecutionDepth
// so it cannot loop indefinitely even if a parent pointer ever becomes
// cyclic.
func (s *Service) GetExecutionAncestors(id string) ([]*api.Execution, error) {
	var rows []*api.Execution
	q := `WITH RECURSIVE chain AS (
		SELECT id, parent_execution_id, 0 AS hops FROM execution WHERE id = $1
		UNION ALL
		SELECT e.id, e.parent_execution_id, chain.hops + 1
		FROM execution e
		JOIN chain ON e.id = chain.parent_execution_id
		WHERE chain.hops < $2
	)
	SELECT ` + treeRowColumns + `,
		(SELECT COUNT(*) FROM execution e2
			WHERE e2.flo_id = e.flo_id AND e2.created_at <= e.created_at) AS sequence
		FROM execution e
		` + treeRowJoins + `
		WHERE e.id IN (SELECT id FROM chain WHERE id != $1)
		ORDER BY e.depth ASC`
	if err := s.conn.Select(&rows, q, id, MaxExecutionDepth+1); err != nil {
		return nil, err
	}
	return rows, nil
}

// GetExecutionDirectChildren returns the immediate children of an
// execution, ordered by creation time so the editor's group rendering
// stays stable across reloads.
func (s *Service) GetExecutionDirectChildren(parentID string) ([]*api.Execution, error) {
	var rows []*api.Execution
	q := `SELECT ` + treeRowColumns + `,
		(SELECT COUNT(*) FROM execution e2
			WHERE e2.flo_id = e.flo_id AND e2.created_at <= e.created_at) AS sequence
		FROM execution e
		` + treeRowJoins + `
		WHERE e.parent_execution_id = $1
		ORDER BY e.created_at ASC`
	if err := s.conn.Select(&rows, q, parentID); err != nil {
		return nil, err
	}
	return rows, nil
}

// mergeDepthCappedFlag returns a parent_metadata blob with the
// depth_capped sentinel set to true, preserving any existing keys the
// caller already populated. Returns a fresh object when no metadata
// was provided.
func mergeDepthCappedFlag(existing json.RawMessage) (json.RawMessage, error) {
	out := map[string]interface{}{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &out); err != nil {
			return nil, fmt.Errorf("invalid parent_metadata: %w", err)
		}
	}
	out["depth_capped"] = true
	return json.Marshal(out)
}
