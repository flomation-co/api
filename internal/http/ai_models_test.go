package http

// Tests for the paste-a-key AI model-options proxies (Anthropic, OpenAI,
// Gemini, Groq, Open WebUI). Each proves the upstream shape is slimmed to the
// {"options":[{name,value}]} option-proxy convention, that non-chat entries
// are filtered where relevant, and that an upstream auth failure surfaces the
// key hint (HTTP 200 + {"error": ...}). Plain api_key values skip the
// environment-secret resolution, so no persistence is wired.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func aiModelsRouter(path string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("account_id", "user-1"); c.Next() })
	r.GET(path, handler)
	return r
}

func getJSON(r *gin.Engine, target string) map[string]interface{} {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	r.ServeHTTP(rec, req)
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body
}

func aiOptionValues(body map[string]interface{}) []string {
	raw, ok := body["options"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, o := range raw {
		out = append(out, o.(map[string]interface{})["value"].(string))
	}
	return out
}

func TestGetAnthropicModels_SlimsAndSorts(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.Expect(r.Header.Get("x-api-key")).To(Equal("sk-ant-test"))
		g.Expect(r.Header.Get("anthropic-version")).To(Equal("2023-06-01"))
		_, _ = w.Write([]byte(`{"data":[
			{"id":"claude-opus-4-8","display_name":"Claude Opus 4.8"},
			{"id":"claude-haiku-4-5","display_name":""}
		]}`))
	}))
	defer upstream.Close()
	prev := anthropicModelsURL
	anthropicModelsURL = upstream.URL
	defer func() { anthropicModelsURL = prev }()

	r := aiModelsRouter("/m", (&Service{}).getAnthropicModels)
	body := getJSON(r, "/m?api_key=sk-ant-test")
	g.Expect(body).To(HaveKey("options"))
	vals := aiOptionValues(body)
	// Sorted case-insensitively by display name. Opus has "Claude Opus 4.8";
	// haiku's display_name is empty so it falls back to its id
	// "claude-haiku-4-5". Comparing lowercased, "claude opus 4.8" < "claude-
	// haiku-4-5" (space 0x20 < hyphen 0x2D), so Opus sorts first.
	g.Expect(vals).To(Equal([]string{"claude-opus-4-8", "claude-haiku-4-5"}))
}

func TestGetOpenAIModels_FiltersToChatModels(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.Expect(r.Header.Get("Authorization")).To(Equal("Bearer sk-openai"))
		_, _ = w.Write([]byte(`{"data":[
			{"id":"gpt-4o"},
			{"id":"o3"},
			{"id":"text-embedding-3-large"},
			{"id":"whisper-1"},
			{"id":"dall-e-3"},
			{"id":"gpt-4o-realtime-preview"}
		]}`))
	}))
	defer upstream.Close()
	prev := openAIModelsURL
	openAIModelsURL = upstream.URL
	defer func() { openAIModelsURL = prev }()

	r := aiModelsRouter("/m", (&Service{}).getOpenAIModels)
	vals := aiOptionValues(getJSON(r, "/m?api_key=sk-openai"))
	// Only the chat families survive; embeddings/whisper/dall-e/realtime drop.
	g.Expect(vals).To(Equal([]string{"gpt-4o", "o3"}))
}

func TestGetGeminiModels_FiltersByGenerateContent(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.Expect(r.Header.Get("x-goog-api-key")).To(Equal("AIza-test"))
		_, _ = w.Write([]byte(`{"models":[
			{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","supportedGenerationMethods":["generateContent"]},
			{"name":"models/text-embedding-004","displayName":"Embedding","supportedGenerationMethods":["embedContent"]}
		]}`))
	}))
	defer upstream.Close()
	prev := geminiModelsURL
	geminiModelsURL = upstream.URL
	defer func() { geminiModelsURL = prev }()

	r := aiModelsRouter("/m", (&Service{}).getGeminiModels)
	body := getJSON(r, "/m?api_key=AIza-test")
	vals := aiOptionValues(body)
	g.Expect(vals).To(Equal([]string{"gemini-2.5-flash"})) // "models/" stripped, embedding dropped
}

func TestGetGroqModels_OpenAIShape(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"llama-3.3-70b-versatile"},{"id":"gpt-oss-120b"}]}`))
	}))
	defer upstream.Close()
	prev := groqModelsURL
	groqModelsURL = upstream.URL
	defer func() { groqModelsURL = prev }()

	r := aiModelsRouter("/m", (&Service{}).getGroqModels)
	vals := aiOptionValues(getJSON(r, "/m?api_key=gsk-test"))
	g.Expect(vals).To(Equal([]string{"gpt-oss-120b", "llama-3.3-70b-versatile"}))
}

func TestGetOpenWebUIModels_EndpointAndData(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.Expect(r.URL.Path).To(Equal("/api/models"))
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model","name":"Local Model"}]}`))
	}))
	defer upstream.Close()

	r := aiModelsRouter("/m", (&Service{}).getOpenWebUIModels)
	body := getJSON(r, "/m?api_key=sk-x&endpoint="+upstream.URL)
	g.Expect(body).To(HaveKey("options"))
	opts := body["options"].([]interface{})
	g.Expect(opts).To(HaveLen(1))
	g.Expect(opts[0].(map[string]interface{})["name"]).To(Equal("Local Model"))
	g.Expect(opts[0].(map[string]interface{})["value"]).To(Equal("local-model"))
}

func TestGetAnthropicModels_UnauthorisedHint(t *testing.T) {
	g := NewWithT(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	prev := anthropicModelsURL
	anthropicModelsURL = upstream.URL
	defer func() { anthropicModelsURL = prev }()

	r := aiModelsRouter("/m", (&Service{}).getAnthropicModels)
	body := getJSON(r, "/m?api_key=bad")
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"].(string)).To(ContainSubstring("check the API key"))
}

func TestResolveOptionAPIKey_RejectsBlankAndManagedCredential(t *testing.T) {
	g := NewWithT(t)
	r := aiModelsRouter("/m", (&Service{}).getOpenAIModels)
	// Blank key — no upstream call, inline error.
	g.Expect(getJSON(r, "/m")).To(HaveKey("error"))
	// Managed credential reference is rejected before any upstream call.
	body := getJSON(r, "/m?api_key=%24%7Bcredentials.OPENAI%7D")
	g.Expect(body).To(HaveKey("error"))
	g.Expect(body["error"].(string)).To(ContainSubstring("Managed credentials"))
}
