package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func TestGatewayAuthTypeValid(t *testing.T) {
	RegisterTestingT(t)
	for _, ok := range []string{"open", "api_key", "basic", "oidc", "flomation"} {
		Expect(gatewayAuthTypeValid(ok)).To(BeTrue(), ok)
	}
	Expect(gatewayAuthTypeValid("mtls")).To(BeFalse())
	Expect(gatewayAuthTypeValid("")).To(BeFalse())
}

func TestGatewayMethodValid(t *testing.T) {
	RegisterTestingT(t)
	Expect(gatewayMethodValid("get")).To(BeTrue())
	Expect(gatewayMethodValid("POST")).To(BeTrue())
	Expect(gatewayMethodValid("TRACE")).To(BeFalse())
}

func TestHashGatewaySecretDeterministicAndSalted(t *testing.T) {
	RegisterTestingT(t)
	h1 := hashGatewaySecret("s3cret", "salt-a")
	Expect(h1).To(Equal(hashGatewaySecret("s3cret", "salt-a"))) // deterministic
	Expect(h1).ToNot(Equal(hashGatewaySecret("s3cret", "salt-b")))
	Expect(h1).ToNot(Equal(hashGatewaySecret("other", "salt-a")))
	Expect(h1).To(HaveLen(64)) // sha256 hex
}

func TestApplyGatewayAuth(t *testing.T) {
	RegisterTestingT(t)

	// nil body ⇒ open, no secret.
	a := &api.GatewayAPI{}
	h, s, verr := applyGatewayAuth(a, nil, true)
	Expect(verr).To(BeEmpty())
	Expect(a.AuthType).To(Equal("open"))
	Expect(h).To(BeNil())
	Expect(s).To(BeNil())

	// api_key on CREATE requires a secret.
	a = &api.GatewayAPI{}
	_, _, verr = applyGatewayAuth(a, &gatewayAuthBody{Type: "api_key"}, true)
	Expect(verr).To(ContainSubstring("secret is required"))

	// api_key WITH a secret hashes it (hash+salt returned; plaintext not stored).
	a = &api.GatewayAPI{}
	h, s, verr = applyGatewayAuth(a, &gatewayAuthBody{Type: "api_key", Secret: "k3y", Config: json.RawMessage(`{"header":"X-API-Key"}`)}, true)
	Expect(verr).To(BeEmpty())
	Expect(a.AuthType).To(Equal("api_key"))
	Expect(h).ToNot(BeNil())
	Expect(s).ToNot(BeNil())
	Expect(*h).To(Equal(hashGatewaySecret("k3y", *s)))
	Expect(string(a.AuthConfig)).To(ContainSubstring("X-API-Key"))

	// oidc is secretless — no secret required even on create.
	a = &api.GatewayAPI{}
	h, s, verr = applyGatewayAuth(a, &gatewayAuthBody{Type: "oidc", Config: json.RawMessage(`{"issuer":"https://id"}`)}, true)
	Expect(verr).To(BeEmpty())
	Expect(h).To(BeNil())
	Expect(s).To(BeNil())

	// api_key on UPDATE without a secret keeps the existing one (nil hash/salt).
	a = &api.GatewayAPI{}
	h, s, verr = applyGatewayAuth(a, &gatewayAuthBody{Type: "api_key"}, false)
	Expect(verr).To(BeEmpty())
	Expect(h).To(BeNil())
	Expect(s).To(BeNil())

	// unknown type is rejected.
	_, _, verr = applyGatewayAuth(&api.GatewayAPI{}, &gatewayAuthBody{Type: "carrier-pigeon"}, false)
	Expect(verr).To(ContainSubstring("unsupported auth type"))
}

// gatewayVerifyMock lets a test control the org-permission lookup.
type gatewayVerifyMock struct {
	mockPersistence
	perms []string
}

func (m *gatewayVerifyMock) GetUserPermissionsInOrganisation(string, string) ([]string, error) {
	return m.perms, nil
}

func verifySessionRequest(svc *Service, body map[string]interface{}) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/internal/gateway/:apiId/verify-session", svc.verifyGatewaySessionInternal)
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/internal/gateway/api-1/verify-session", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestVerifyGatewaySession(t *testing.T) {
	RegisterTestingT(t)
	orig := resolveUserFromToken
	defer func() { resolveUserFromToken = orig }()

	// The token resolves to user "u-1"; an empty token resolves to nothing.
	resolveUserFromToken = func(_ string, token string) (string, error) {
		if token == "good" {
			return "u-1", nil
		}
		return "", nil
	}

	// Personal-scoped: only the owner passes.
	svc := setupTestService(&mockPersistence{})
	svc.config = &config.Config{}
	w := verifySessionRequest(svc, map[string]interface{}{"token": "good", "owner_id": "u-1"})
	Expect(w.Body.String()).To(ContainSubstring(`"ok":true`))
	w = verifySessionRequest(svc, map[string]interface{}{"token": "good", "owner_id": "someone-else"})
	Expect(w.Body.String()).To(ContainSubstring(`"ok":false`))

	// Invalid token ⇒ denied.
	w = verifySessionRequest(svc, map[string]interface{}{"token": "bad", "owner_id": "u-1"})
	Expect(w.Body.String()).To(ContainSubstring(`"ok":false`))

	// Org-scoped member with the required permission ⇒ ok.
	member := &gatewayVerifyMock{perms: []string{"gateway.view", "flow.execute"}}
	svc = setupTestService(&member.mockPersistence)
	svc.persistence = member
	svc.config = &config.Config{}
	w = verifySessionRequest(svc, map[string]interface{}{"token": "good", "organisation_id": "org-1", "required_permission": "flow.execute"})
	Expect(w.Body.String()).To(ContainSubstring(`"ok":true`))

	// Org member LACKING the required permission ⇒ denied.
	w = verifySessionRequest(svc, map[string]interface{}{"token": "good", "organisation_id": "org-1", "required_permission": "organisation.manage"})
	Expect(w.Body.String()).To(ContainSubstring(`"ok":false`))

	// Non-member (no perms) ⇒ denied.
	nonMember := &gatewayVerifyMock{perms: nil}
	svc = setupTestService(&nonMember.mockPersistence)
	svc.persistence = nonMember
	svc.config = &config.Config{}
	w = verifySessionRequest(svc, map[string]interface{}{"token": "good", "organisation_id": "org-1"})
	Expect(w.Body.String()).To(ContainSubstring(`"ok":false`))
}
