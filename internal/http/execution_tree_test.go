package http

// Handler-level coverage for the three hierarchical executions
// endpoints introduced in commit 3 of the execution-hierarchy M0
// plan:
//
//   GET /api/v1/execution-tree/:rootID
//   GET /api/v1/execution/:id/ancestors
//   GET /api/v1/execution/:id/children
//
// The mockPersistence already implements the three persistence
// methods these handlers call. The auth context is set to a personal-
// mode user (no organisations) so the RBAC layer waves the request
// through. Per-row visibility is exercised by giving the lookup row
// an organisation_id the user doesn't share.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

const (
	treeTestUserID = "user-1"
)

// setupTreeRouter wires the three endpoints behind a middleware that
// stamps the gin context with account_id, mirroring what jwtMiddleware
// does in production but without the JWT plumbing.
func setupTreeRouter(svc *Service) *gin.Engine {
	r := gin.New()
	auth := func(c *gin.Context) {
		c.Set("account_id", treeTestUserID)
		c.Next()
	}
	r.GET("/api/v1/execution-tree/:rootID", auth, svc.getExecutionTree)
	r.GET("/api/v1/execution/:id/ancestors", auth, svc.getExecutionAncestors)
	r.GET("/api/v1/execution/:id/children", auth, svc.getExecutionChildren)
	return r
}

// seedTreeFixture wires a three-level tree into the mock:
//
//	root (depth 0)
//	├── child-a (depth 1)
//	│   └── grandchild (depth 2)
//	└── child-b (depth 1)
//
// All rows live in personal mode (organisation_id = nil) and are
// owned by treeTestUserID so the personal-mode access check passes.
func seedTreeFixture(m *mockPersistence) {
	m.users[treeTestUserID] = &api.User{ID: treeTestUserID, Name: "Test"}

	rootID := "root-1"
	childAID := "child-a"
	childBID := "child-b"
	grandID := "grandchild"

	makeExec := func(id, parent string, depth int) *api.Execution {
		e := &api.Execution{
			ID:              id,
			FloID:           "flo-1",
			OwnerID:         treeTestUserID,
			Depth:           depth,
			RootExecutionID: rootID,
		}
		if parent != "" {
			p := parent
			e.ParentExecutionID = &p
		}
		return e
	}

	m.executions[rootID] = makeExec(rootID, "", 0)
	m.executions[childAID] = makeExec(childAID, rootID, 1)
	m.executions[childBID] = makeExec(childBID, rootID, 1)
	m.executions[grandID] = makeExec(grandID, childAID, 2)
}

func decodeExecList(t *testing.T, body []byte) []*api.Execution {
	t.Helper()
	var out []*api.Execution
	Expect(json.Unmarshal(body, &out)).To(Succeed())
	return out
}

func TestGetExecutionTree_ReturnsWholeSubtree(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	seedTreeFixture(mock)

	svc := setupTestService(mock)
	router := setupTreeRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/execution-tree/root-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	tree := decodeExecList(t, rec.Body.Bytes())
	Expect(tree).To(HaveLen(4), "fixture has root + 2 children + 1 grandchild")

	ids := map[string]bool{}
	for _, e := range tree {
		ids[e.ID] = true
	}
	Expect(ids).To(HaveKey("root-1"))
	Expect(ids).To(HaveKey("child-a"))
	Expect(ids).To(HaveKey("child-b"))
	Expect(ids).To(HaveKey("grandchild"))
}

func TestGetExecutionAncestors_WalksUpFromLeaf(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	seedTreeFixture(mock)

	svc := setupTestService(mock)
	router := setupTreeRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/execution/grandchild/ancestors", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	chain := decodeExecList(t, rec.Body.Bytes())
	Expect(chain).To(HaveLen(2), "ancestors of a depth-2 leaf must be root + parent, in root-first order")
	Expect(chain[0].ID).To(Equal("root-1"))
	Expect(chain[1].ID).To(Equal("child-a"))
}

func TestGetExecutionChildren_ReturnsOnlyDirectChildren(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	seedTreeFixture(mock)

	svc := setupTestService(mock)
	router := setupTreeRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/execution/root-1/children", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusOK))
	kids := decodeExecList(t, rec.Body.Bytes())
	Expect(kids).To(HaveLen(2), "root has 2 direct children; the grandchild belongs to child-a, not root")

	for _, c := range kids {
		Expect(c.ParentExecutionID).NotTo(BeNil())
		Expect(*c.ParentExecutionID).To(Equal("root-1"))
	}
}

func TestGetExecutionTree_NotFound_Returns404(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.users[treeTestUserID] = &api.User{ID: treeTestUserID}

	svc := setupTestService(mock)
	router := setupTreeRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/execution-tree/does-not-exist", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusNotFound),
		"unknown root must 404 before any tree fetch happens — keeps the visibility check ahead of the data read")
}

func TestGetExecutionAncestors_ForbiddenAcrossOrgs(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newMockPersistence()
	mock.users[treeTestUserID] = &api.User{ID: treeTestUserID} // personal mode

	otherOrg := "org-other"
	mock.executions["secret-leaf"] = &api.Execution{
		ID:              "secret-leaf",
		FloID:           "flo-x",
		OwnerID:         "another-user",
		OrganisationID:  &otherOrg, // user can't see this in personal mode
		RootExecutionID: "secret-root",
	}

	svc := setupTestService(mock)
	router := setupTreeRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/execution/secret-leaf/ancestors", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	Expect(rec.Code).To(Equal(http.StatusForbidden),
		"personal-mode users must not be able to read tree data from other orgs")
}
