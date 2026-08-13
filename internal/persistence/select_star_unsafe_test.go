package persistence

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"

	api "flomation.app/automate/api"
)

// TestSelectStarToleratesUnmappedColumns is the regression guard for the
// outage where adding conversation-search columns (content_tsv, embedding) to
// agent_message made every `SELECT *` reader of that table fail its scan with
// "missing destination name <column>" — silently wiping every agent's
// conversation memory (GetAgentConversationMessages returned an error that
// callers swallowed, so turns ran context-blind).
//
// NewService marks the sqlx connection Unsafe so scans ignore columns the
// destination struct does not map. This test pins that behaviour and documents
// the failure mode a *safe* connection exhibits, so the fix cannot silently
// regress.
func TestSelectStarToleratesUnmappedColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	// The columns AgentMessage maps, PLUS the two search columns it deliberately
	// does not — the exact shape migrations 146/147 produced.
	cols := []string{
		"id", "agent_id", "session_id", "conversation_id", "sequence",
		"direction", "channel_type", "sender", "content", "metadata",
		"execution_id", "source_conversation_id", "created_at",
		"content_tsv", "embedding",
	}
	newRows := func() *sqlmock.Rows {
		return sqlmock.NewRows(cols).AddRow(
			"m1", "a1", nil, "c1", int64(1),
			"inbound", "slack", nil, "hi", nil,
			nil, nil, time.Now(),
			"'hi':1", "[0.1,0.2]",
		)
	}

	sx := sqlx.NewDb(db, "postgres")

	// A SAFE connection fails on the unmapped columns — the pre-fix behaviour
	// that caused the outage.
	mock.ExpectQuery("SELECT").WillReturnRows(newRows())
	var safeOut []*api.AgentMessage
	if err := sx.Select(&safeOut, "SELECT * FROM agent_message"); err == nil {
		t.Fatal("expected a safe scan to fail on unmapped columns, but it succeeded")
	}

	// An UNSAFE connection (what NewService configures) tolerates them.
	mock.ExpectQuery("SELECT").WillReturnRows(newRows())
	var out []*api.AgentMessage
	if err := sx.Unsafe().Select(&out, "SELECT * FROM agent_message"); err != nil {
		t.Fatalf("unsafe scan must tolerate unmapped columns, got: %v", err)
	}
	if len(out) != 1 || out[0].Content != "hi" {
		t.Fatalf("expected one row with content \"hi\", got %+v", out)
	}
}
