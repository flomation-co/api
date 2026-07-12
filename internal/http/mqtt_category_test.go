package http

import "testing"

// The editor's palette is built entirely from the maps in this file — the
// executor manifest's own category data is ignored at serve time. A missing
// entry therefore does not fail anywhere; the actions simply resolve no category
// and vanish from the palette. This guards the Message Brokers ▸ MQTT grouping
// against that silent failure.
func TestMqttActionsResolveTheirCategory(t *testing.T) {
	cat := getCategoryForAction("messagebrokers/mqtt/publish")
	if cat == nil {
		t.Fatal("messagebrokers categoryMetadata entry is missing — 3-segment messagebrokers/* IDs resolve no category without it, and the MQTT actions disappear from the palette")
	}

	if cat.Name != "Message Brokers" {
		t.Errorf("category name = %q, want %q", cat.Name, "Message Brokers")
	}
	if cat.SubName != "MQTT" {
		t.Errorf("sub-category name = %q, want MQTT — without a subCategoryMetadata entry it would be auto-title-cased to %q with no icon", cat.SubName, "Mqtt")
	}
	if cat.SubIcon != "tower-broadcast" {
		t.Errorf("sub-category icon = %q, want tower-broadcast", cat.SubIcon)
	}
}

// Every MQTT action must land in the same sub-group, or they scatter across the
// palette.
func TestEveryMqttActionSharesOneSubGroup(t *testing.T) {
	actions := []string{
		"messagebrokers/mqtt/publish",
		"messagebrokers/mqtt/message_wait",
		"messagebrokers/mqtt/retained_get",
		"messagebrokers/mqtt/retained_clear",
		"messagebrokers/mqtt/request_reply",
	}

	for _, id := range actions {
		cat := getCategoryForAction(id)
		if cat == nil {
			t.Fatalf("%s resolved no category", id)
		}
		if cat.Key != "messagebrokers" || cat.SubKey != "messagebrokers/mqtt" {
			t.Errorf("%s resolved to %s/%s, want messagebrokers/messagebrokers/mqtt", id, cat.Key, cat.SubKey)
		}
	}
}
