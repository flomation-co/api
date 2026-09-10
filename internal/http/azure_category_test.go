package http

import "testing"

// The editor's palette is built entirely from the maps in action.go — the
// executor manifest's own category consts are parsed and then ignored at serve
// time. A missing entry does not fail anywhere; the actions simply resolve no
// category and vanish from the palette. These tests guard the Azure groupings
// against that silent failure.
func TestAzureActionsResolveTheirCategory(t *testing.T) {
	cat := getCategoryForAction("azure/storage/blob_upload")
	if cat == nil {
		t.Fatal("azure categoryMetadata entry is missing — 3-segment azure/* IDs resolve no category without it, and the Azure actions disappear from the palette")
	}
	if cat.Key != "azure" || cat.Name != "Azure" {
		t.Errorf("category = %s (%q), want azure (%q)", cat.Key, cat.Name, "Azure")
	}
	if cat.Icon != "azure" {
		t.Errorf("category icon = %q, want azure", cat.Icon)
	}
	if cat.Description != "Microsoft Azure integrations" {
		t.Errorf("category description = %q, want %q", cat.Description, "Microsoft Azure integrations")
	}
}

// The three Azure sub-groups must carry the exact names/icons/descriptions the
// spec pins (byte-identical to the executor category.go consts) — without a
// subCategoryMetadata entry the sub-name would be auto-title-cased from the
// directory ("Cosmosdb", "Entra") with no icon.
func TestAzureSubCategoriesMatchTheExecutorConsts(t *testing.T) {
	for _, tc := range []struct {
		actionID    string
		subKey      string
		subName     string
		subIcon     string
		description string
	}{
		{
			actionID:    "azure/storage/container_create",
			subKey:      "azure/storage",
			subName:     "Storage",
			subIcon:     "box-archive",
			description: "Azure Blob Storage — containers, blobs, tiers, tags, and shared access links",
		},
		{
			actionID:    "azure/cosmosdb/item_query",
			subKey:      "azure/cosmosdb",
			subName:     "Cosmos DB",
			subIcon:     "database",
			description: "Azure Cosmos DB (NoSQL) — databases, containers, items, queries, and throughput",
		},
		{
			actionID:    "azure/entra/user_create",
			subKey:      "azure/entra",
			subName:     "Entra ID",
			subIcon:     "id-badge",
			description: "Microsoft Entra ID (Azure AD) — users, groups, membership, licences, and guest invites",
		},
	} {
		cat := getCategoryForAction(tc.actionID)
		if cat == nil {
			t.Fatalf("%s resolved no category", tc.actionID)
		}
		if cat.Key != "azure" {
			t.Errorf("%s: key = %q, want azure", tc.actionID, cat.Key)
		}
		if cat.SubKey != tc.subKey {
			t.Errorf("%s: sub-key = %q, want %q", tc.actionID, cat.SubKey, tc.subKey)
		}
		if cat.SubName != tc.subName {
			t.Errorf("%s: sub-name = %q, want %q", tc.actionID, cat.SubName, tc.subName)
		}
		if cat.SubIcon != tc.subIcon {
			t.Errorf("%s: sub-icon = %q, want %q", tc.actionID, cat.SubIcon, tc.subIcon)
		}
		if cat.SubDescription != tc.description {
			t.Errorf("%s: sub-description = %q, want %q", tc.actionID, cat.SubDescription, tc.description)
		}
	}
}

// Azure AI Search is a vector-database capability, so it sits under the
// pre-existing Vector Database category as a sibling of pgvector — not under
// Azure.
func TestAzureAISearchLandsUnderVectorDatabase(t *testing.T) {
	cat := getCategoryForAction("vectordatabase/azureaisearch/search")
	if cat == nil {
		t.Fatal("vectordatabase/azureaisearch/search resolved no category")
	}
	if cat.Key != "vectordatabase" || cat.Name != "Vector Database" {
		t.Errorf("category = %s (%q), want vectordatabase (%q)", cat.Key, cat.Name, "Vector Database")
	}
	if cat.SubKey != "vectordatabase/azureaisearch" {
		t.Errorf("sub-key = %q, want vectordatabase/azureaisearch", cat.SubKey)
	}
	if cat.SubName != "Azure AI Search" {
		t.Errorf("sub-name = %q, want %q — without a subCategoryMetadata entry it would be auto-title-cased to %q with no icon", cat.SubName, "Azure AI Search", "Azureaisearch")
	}
	if cat.SubIcon != "magnifying-glass" {
		t.Errorf("sub-icon = %q, want magnifying-glass", cat.SubIcon)
	}
	if cat.SubDescription != "Azure AI Search — manage indexes and documents, and run keyword, vector, and hybrid queries" {
		t.Errorf("sub-description = %q", cat.SubDescription)
	}
}

// The Azure OpenAI chat node is a 2-segment ID under the pre-existing "ai"
// category, so it needs no category work of its own. This pins that: if
// someone "helpfully" moves it under azure/, the node moves out of AI in the
// palette (and its dynamicOptionsMetadata marker key stops matching).
func TestAzureOpenAIStaysInTheAiCategory(t *testing.T) {
	cat := getCategoryForAction("ai/azure_openai")
	if cat == nil {
		t.Fatal("ai/azure_openai resolved no category")
	}
	if cat.Key != "ai" || cat.Name != "AI" {
		t.Errorf("ai/azure_openai resolved to %s (%q), want ai (%q)", cat.Key, cat.Name, "AI")
	}
	// Two segments — getCategoryForAction only populates Sub* for 3+ segments,
	// so it sits directly under AI with no sub-group.
	if cat.SubKey != "" || cat.SubName != "" {
		t.Errorf("ai/azure_openai gained a sub-group %s/%q — it must sit directly under AI", cat.SubKey, cat.SubName)
	}
}

// Every azure/* action of each sub-service must land in the same sub-group, or
// they scatter across the palette.
func TestEveryAzureActionSharesItsSubGroup(t *testing.T) {
	for subKey, actions := range map[string][]string{
		"azure/storage": {
			"container_create", "container_get", "container_get_all", "container_delete",
			"container_set_metadata", "blob_upload", "blob_upload_from_url", "blob_download",
			"blob_get_properties", "blob_get_all", "blob_delete", "blob_copy", "blob_snapshot",
			"blob_undelete", "blob_set_tier", "blob_set_metadata", "blob_set_properties",
			"blob_get_tags", "blob_set_tags", "blob_find_by_tags", "blob_generate_sas",
		},
		"azure/cosmosdb": {
			"database_create", "database_get", "database_get_all", "database_delete",
			"container_create", "container_get", "container_get_all", "container_replace",
			"container_delete", "throughput_get", "throughput_update", "item_create",
			"item_get", "item_get_all", "item_query", "item_replace", "item_patch", "item_delete",
		},
		"azure/entra": {
			"user_create", "user_get", "user_get_all", "user_update", "user_delete",
			"user_add_to_group", "user_remove_from_group", "user_list_groups",
			"user_check_group_membership", "user_assign_license", "user_revoke_sessions",
			"user_get_manager", "user_set_manager", "group_create", "group_get",
			"group_get_all", "group_update", "group_delete", "group_list_members",
			"group_add_members", "group_remove_member", "group_list_owners",
			"guest_invite", "deleted_item_restore", "subscribed_skus_get_all",
		},
	} {
		for _, action := range actions {
			id := subKey + "/" + action
			cat := getCategoryForAction(id)
			if cat == nil {
				t.Fatalf("%s resolved no category", id)
			}
			if cat.Key != "azure" || cat.SubKey != subKey {
				t.Errorf("%s resolved to %s/%s, want azure/%s", id, cat.Key, cat.SubKey, subKey)
			}
		}
	}
}
