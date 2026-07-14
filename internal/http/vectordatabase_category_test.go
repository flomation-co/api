package http

import "testing"

// The editor's palette is built entirely from the maps in action.go — the
// executor manifest's own category consts are parsed and then ignored at serve
// time (api.ActionDefinition has no Category field at all). A missing entry
// therefore does not fail anywhere; the actions simply resolve no category and
// vanish from the palette. This guards the Vector Database ▸ pgvector grouping
// against that silent failure.
func TestVectorDatabaseActionsResolveTheirCategory(t *testing.T) {
	cat := getCategoryForAction("vectordatabase/pgvector/document_search")
	if cat == nil {
		t.Fatal("vectordatabase categoryMetadata entry is missing — 3-segment vectordatabase/* IDs resolve no category without it, and the pgvector actions disappear from the palette")
	}

	if cat.Name != "Vector Database" {
		t.Errorf("category name = %q, want %q", cat.Name, "Vector Database")
	}
	if cat.Icon != "circle-nodes" {
		t.Errorf("category icon = %q, want circle-nodes", cat.Icon)
	}
	if cat.SubName != "pgvector" {
		t.Errorf("sub-category name = %q, want pgvector — without a subCategoryMetadata entry it would be auto-title-cased to %q with no icon", cat.SubName, "Pgvector")
	}
	if cat.SubIcon != "database" {
		t.Errorf("sub-category icon = %q, want database", cat.SubIcon)
	}
}

// Every pgvector action must land in the same sub-group, or they scatter across
// the palette.
func TestEveryPgvectorActionSharesOneSubGroup(t *testing.T) {
	actions := []string{
		"vectordatabase/pgvector/table_create",
		"vectordatabase/pgvector/table_info",
		"vectordatabase/pgvector/index_create",
		"vectordatabase/pgvector/document_insert",
		"vectordatabase/pgvector/document_search",
		"vectordatabase/pgvector/document_upsert",
		"vectordatabase/pgvector/document_update",
		"vectordatabase/pgvector/document_delete",
		"vectordatabase/pgvector/document_get",
		"vectordatabase/pgvector/document_list",
		"vectordatabase/pgvector/document_count",
		"vectordatabase/pgvector/hybrid_search",
	}

	for _, id := range actions {
		cat := getCategoryForAction(id)
		if cat == nil {
			t.Fatalf("%s resolved no category", id)
		}
		if cat.Key != "vectordatabase" || cat.SubKey != "vectordatabase/pgvector" {
			t.Errorf("%s resolved to %s/%s, want vectordatabase/vectordatabase/pgvector", id, cat.Key, cat.SubKey)
		}
	}
}

// ai/embed_text ships alongside the pgvector node but is a 2-segment ID under
// the pre-existing "ai" category, so it needs no category work of its own. This
// pins that: if someone "helpfully" moves it under vectordatabase/, or adds an
// ai/embed sub-group, the embedding node moves out of AI in the palette.
func TestEmbedTextStaysInTheAiCategory(t *testing.T) {
	cat := getCategoryForAction("ai/embed_text")
	if cat == nil {
		t.Fatal("ai/embed_text resolved no category")
	}
	if cat.Key != "ai" || cat.Name != "AI" {
		t.Errorf("ai/embed_text resolved to %s (%q), want ai (%q)", cat.Key, cat.Name, "AI")
	}
	// Two segments — getCategoryForAction only populates Sub* for 3+ segments,
	// so it sits directly under AI with no sub-group.
	if cat.SubKey != "" || cat.SubName != "" {
		t.Errorf("ai/embed_text gained a sub-group %s/%q — it must sit directly under AI", cat.SubKey, cat.SubName)
	}
}
