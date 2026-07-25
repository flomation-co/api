package http

import "testing"

// The editor's palette is built entirely from the maps in action.go — the
// executor manifest's own category consts are parsed and then ignored at serve
// time (api.ActionDefinition has no Category field at all). A missing entry
// therefore does not fail anywhere; the actions simply resolve no category and
// vanish from the palette. This guards the CRM ▸ Salesforce grouping against
// that silent failure.
func TestSalesforceActionsResolveTheirCategory(t *testing.T) {
	cat := getCategoryForAction("crm/salesforce/lead_create")
	if cat == nil {
		t.Fatal("crm categoryMetadata entry is missing — 3-segment crm/* IDs resolve no category without it, and every Salesforce action disappears from the palette")
	}

	if cat.Key != "crm" {
		t.Errorf("category key = %q, want crm", cat.Key)
	}
	if cat.Name != "CRM" {
		t.Errorf("category name = %q, want CRM", cat.Name)
	}
	if cat.Icon != "people-group" {
		t.Errorf("category icon = %q, want people-group", cat.Icon)
	}
	if cat.SubKey != "crm/salesforce" {
		t.Errorf("sub-category key = %q, want crm/salesforce", cat.SubKey)
	}
	if cat.SubName != "Salesforce" {
		t.Errorf("sub-category name = %q, want Salesforce — without a subCategoryMetadata entry it would be auto-title-cased with no icon", cat.SubName)
	}
	if cat.SubIcon != "salesforce" {
		t.Errorf("sub-category icon = %q, want salesforce", cat.SubIcon)
	}
}

// HubSpot keeps its 2-segment IDs (hubspot/contact_create) and is remapped onto
// the SAME "crm" Key, so both providers share one palette header. This is the
// assertion that actually earns its keep: the two entries must agree
// byte-for-byte on Key/Name/Icon/Description, because whichever action the
// editor reads first supplies the group header for both. Any drift and the CRM
// header changes depending on load order — a bug that reproduces intermittently
// and looks like a rendering fault.
func TestHubSpotAndSalesforceShareOneCRMHeader(t *testing.T) {
	hub := getCategoryForAction("hubspot/contact_create")
	sf := getCategoryForAction("crm/salesforce/lead_create")

	if hub == nil {
		t.Fatal("hubspot/* resolved no category — the CRM remap entry has been removed")
	}
	if sf == nil {
		t.Fatal("crm/* resolved no category")
	}

	if hub.Key != sf.Key {
		t.Errorf("HubSpot resolves to key %q but Salesforce to %q — they must share one CRM group", hub.Key, sf.Key)
	}
	if hub.Name != sf.Name {
		t.Errorf("group name drift: HubSpot %q vs Salesforce %q", hub.Name, sf.Name)
	}
	if hub.Icon != sf.Icon {
		t.Errorf("group icon drift: HubSpot %q vs Salesforce %q", hub.Icon, sf.Icon)
	}
	if hub.Description != sf.Description {
		t.Errorf("group description drift:\n  HubSpot:    %q\n  Salesforce: %q", hub.Description, sf.Description)
	}

	// HubSpot carries its sub-group inline (2-segment IDs never reach
	// subCategoryMetadata); Salesforce resolves its own. They must stay
	// distinct sub-groups under the shared header.
	if hub.SubKey == sf.SubKey {
		t.Errorf("HubSpot and Salesforce collapsed into one sub-group (%q)", hub.SubKey)
	}
	if hub.SubName != "HubSpot" {
		t.Errorf("HubSpot sub-group name = %q, want HubSpot", hub.SubName)
	}
}

// Every Salesforce action must land in the same sub-group, or they scatter
// across the palette. A representative action from each palette group is
// enough: the resolution is purely prefix-based, so one per group proves the
// whole group.
func TestEverySalesforceActionSharesOneSubGroup(t *testing.T) {
	actions := []string{
		"crm/salesforce/lead_create",
		"crm/salesforce/lead_convert",
		"crm/salesforce/contact_upsert",
		"crm/salesforce/account_get_all",
		"crm/salesforce/opportunity_update",
		"crm/salesforce/case_delete",
		"crm/salesforce/task_create",
		"crm/salesforce/campaign_member_create",
		"crm/salesforce/file_upload",
		"crm/salesforce/user_get_all",
		"crm/salesforce/custom_object_create",
		"crm/salesforce/record_create",
		"crm/salesforce/search_soql",
		"crm/salesforce/org_limits_get",
	}

	for _, id := range actions {
		cat := getCategoryForAction(id)
		if cat == nil {
			t.Fatalf("%s resolved no category", id)
		}
		if cat.Key != "crm" || cat.SubKey != "crm/salesforce" {
			t.Errorf("%s resolved to %s/%s, want crm/crm/salesforce", id, cat.Key, cat.SubKey)
		}
	}
}

// The palette is only two tiers deep — a third level is silently flattened. The
// 17 conceptual groups (Leads, Contacts, Opportunities, ...) are therefore a
// naming convention inside ONE Salesforce sub-group, not real palette nodes.
// This asserts the shape we actually get, so nobody later "fixes" the grouping
// by adding a segment and quietly loses it.
func TestSalesforceGroupingIsTwoTiersOnly(t *testing.T) {
	cat := getCategoryForAction("crm/salesforce/lead_create")
	if cat == nil {
		t.Fatal("crm/salesforce/lead_create resolved no category")
	}
	// A hypothetical third tier must not appear as a deeper sub-key.
	deeper := getCategoryForAction("crm/salesforce/leads/lead_create")
	if deeper != nil && deeper.SubKey != "crm/salesforce" {
		t.Errorf("a 4-segment ID resolved to sub-key %q — the palette is 2-tier and a third level is silently flattened; do not group actions by adding path segments", deeper.SubKey)
	}
}
