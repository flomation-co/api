package http

// Tests for the HeyGen Avatar/Voice option proxies. They prove the X-Api-Key
// header is forwarded, records under data.<list> are mapped to the
// {name,value} option-proxy convention (voice labels annotated with language),
// and an upstream auth failure surfaces the key hint. Plain api_key values skip
// the environment-secret resolution, so no persistence is wired. Reuses the
// aiModelsRouter/getJSON/aiOptionValues helpers from ai_models_test.go.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/gomega"
)

func TestGetHeyGenAvatars_MapsAndSorts(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.Expect(r.Header.Get("X-Api-Key")).To(Equal("hg-key"))
		_, _ = w.Write([]byte(`{"data":{"avatars":[
			{"avatar_id":"zoe","avatar_name":"Zoe"},
			{"avatar_id":"amir","avatar_name":"Amir"},
			{"avatar_id":"noname"}
		]}}`))
	}))
	defer upstream.Close()
	prev := heygenAvatarsURL
	heygenAvatarsURL = upstream.URL
	defer func() { heygenAvatarsURL = prev }()

	r := aiModelsRouter("/a", (&Service{}).getHeyGenAvatars)
	body := getJSON(r, "/a?api_key=hg-key")
	g.Expect(body).To(HaveKey("options"))
	// Sorted case-insensitively by name; the id-only record falls back to its id.
	g.Expect(aiOptionValues(body)).To(Equal([]string{"amir", "noname", "zoe"}))
}

func TestGetHeyGenVoices_AnnotatesLanguage(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"voices":[
			{"voice_id":"v1","name":"Ada","language":"English"}
		]}}`))
	}))
	defer upstream.Close()
	prev := heygenVoicesURL
	heygenVoicesURL = upstream.URL
	defer func() { heygenVoicesURL = prev }()

	r := aiModelsRouter("/v", (&Service{}).getHeyGenVoices)
	body := getJSON(r, "/v?api_key=hg-key")
	opts := body["options"].([]interface{})
	g.Expect(opts).To(HaveLen(1))
	first := opts[0].(map[string]interface{})
	g.Expect(first["value"]).To(Equal("v1"))
	g.Expect(first["name"]).To(Equal("Ada (English)"))
}

func TestGetHeyGenAvatars_AuthFailureHint(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	prev := heygenAvatarsURL
	heygenAvatarsURL = upstream.URL
	defer func() { heygenAvatarsURL = prev }()

	r := aiModelsRouter("/a", (&Service{}).getHeyGenAvatars)
	body := getJSON(r, "/a?api_key=bad")
	g.Expect(body).ToNot(HaveKey("options"))
	g.Expect(body["error"]).To(ContainSubstring("check the API key"))
}
