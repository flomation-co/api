package http

import "testing"

// The editor palette is built entirely from the maps in action.go — the
// executor manifest's category consts are ignored at serve time. Without the
// heygen entries, the 3-segment heygen/* IDs resolve no category and every
// HeyGen action vanishes from the palette. This guards HeyGen ▸ Videos.
func TestHeyGenActionsResolveTheirCategory(t *testing.T) {
	cat := getCategoryForAction("heygen/videos/generate_avatar_video")
	if cat == nil {
		t.Fatal("heygen categoryMetadata entry is missing — HeyGen actions disappear from the palette without it")
	}
	if cat.Key != "heygen" {
		t.Errorf("category key = %q, want heygen", cat.Key)
	}
	if cat.Name != "HeyGen" {
		t.Errorf("category name = %q, want HeyGen", cat.Name)
	}
	if cat.Icon != "heygen" {
		t.Errorf("category icon = %q, want heygen", cat.Icon)
	}
	if cat.SubKey != "heygen/videos" {
		t.Errorf("sub-category key = %q, want heygen/videos", cat.SubKey)
	}
	if cat.SubName != "Videos" {
		t.Errorf("sub-category name = %q, want Videos — without a subCategoryMetadata entry it would be auto-title-cased with no icon", cat.SubName)
	}
	if cat.SubIcon != "video" {
		t.Errorf("sub-category icon = %q, want video", cat.SubIcon)
	}
}

func TestHeyGenSubCategoriesAllResolve(t *testing.T) {
	cases := map[string]struct{ subKey, subName, subIcon string }{
		"heygen/avatars/list_avatars": {"heygen/avatars", "Avatars", "user"},
		"heygen/voices/list_voices":   {"heygen/voices", "Voices", "microphone"},
		"heygen/account/get_credits":  {"heygen/account", "Account", "gauge"},
	}
	for id, want := range cases {
		cat := getCategoryForAction(id)
		if cat == nil {
			t.Fatalf("%s resolved no category", id)
		}
		if cat.SubKey != want.subKey || cat.SubName != want.subName || cat.SubIcon != want.subIcon {
			t.Errorf("%s: got sub (%q,%q,%q), want (%q,%q,%q)", id, cat.SubKey, cat.SubName, cat.SubIcon, want.subKey, want.subName, want.subIcon)
		}
	}
}
