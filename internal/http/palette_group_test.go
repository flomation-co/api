package http

import "testing"

// TestEveryCategoryHasAGroup is the guard that makes the group layer safe to
// add to. A category with no entry in groupByCategory gets no group fields, so
// it would drop out of the Add Node menu's top tier — visible to nobody until
// a user went looking for an integration that had quietly vanished.
//
// Adding a category to the executor therefore has to be paired with a line
// here, and this test is how you find that out.
func TestEveryCategoryHasAGroup(t *testing.T) {
	for slug := range categoryMetadata {
		key, ok := groupByCategory[slug]
		if !ok {
			t.Errorf("category %q has no palette group — add it to groupByCategory", slug)
			continue
		}
		if _, ok := groupMetadata[key]; !ok {
			t.Errorf("category %q maps to unknown group %q", slug, key)
		}
	}
}

// TestGroupOrderIsATotalOrder keeps the menu's top tier deterministic. Two
// groups sharing an order number would sort arbitrarily and the tiles would
// move between page loads.
func TestGroupOrderIsATotalOrder(t *testing.T) {
	seen := map[int]string{}
	for key, g := range groupMetadata {
		if g.Order < 1 {
			t.Errorf("group %q has no order", key)
		}
		if other, clash := seen[g.Order]; clash {
			t.Errorf("groups %q and %q share order %d", key, other, g.Order)
		}
		seen[g.Order] = key
	}
	if len(seen) != len(groupMetadata) {
		t.Errorf("expected %d distinct orders, got %d", len(groupMetadata), len(seen))
	}
}

// TestCloudIsLast pins the decision rather than leaving it to whoever next
// edits the map. Cloud is 59% of the catalogue and irrelevant to most people;
// putting it anywhere but last undoes the point of grouping at all.
func TestCloudIsLast(t *testing.T) {
	cloud := groupMetadata["cloud"].Order
	for key, g := range groupMetadata {
		if key != "cloud" && g.Order >= cloud {
			t.Errorf("group %q (order %d) is at or after Cloud & data (%d)", key, g.Order, cloud)
		}
	}
}

// TestBuildingBlocksLeads — triggers, conditionals and outputs are the editor's
// own vocabulary and the most-reached-for things in the product.
func TestBuildingBlocksLeads(t *testing.T) {
	if groupMetadata["building-blocks"].Order != 1 {
		t.Errorf("Building blocks should lead, got order %d", groupMetadata["building-blocks"].Order)
	}
}

// TestGroupOverridesBeatCategory covers the services whose job differs from
// their vendor: Google Calendar is a calendar even though Google's other
// actions are documents.
func TestGroupOverridesBeatCategory(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"google/calendar/event_create", "calendars"},
		{"google/docs/document_create", "documents"},
		{"microsoft/outlook/mail_send", "messaging"},
		{"microsoft/excel/worksheet_add", "documents"},
		{"slack/send_message", "messaging"},
		{"trigger/schedule", "building-blocks"},
		{"aws/s3/put", "cloud"},
		{"oracle/compute/instance_list", "cloud"},
		{"forms/typeform/form_create", "forms"},
		{"ukgov/companieshouse/company_profile", "government"},
	}
	for _, c := range cases {
		cat := getCategoryForAction(c.id)
		if cat == nil {
			t.Errorf("%s resolved to no category", c.id)
			continue
		}
		if cat.GroupKey != c.want {
			t.Errorf("%s -> group %q, want %q", c.id, cat.GroupKey, c.want)
		}
		if cat.GroupName == "" || cat.GroupOrder == 0 {
			t.Errorf("%s -> group fields incomplete: %+v", c.id, cat)
		}
	}
}

// TestUnknownCategoryStillResolvesNothing — the group layer must not change
// what happens for an action id the API does not recognise.
func TestUnknownCategoryHasNoGroup(t *testing.T) {
	if cat := getCategoryForAction("notarealcategory/thing"); cat != nil {
		t.Errorf("unknown category should resolve to nil, got %+v", cat)
	}
}
