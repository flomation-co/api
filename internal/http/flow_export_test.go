package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flomation.app/automate/api"
	pgvector "github.com/pgvector/pgvector-go"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"
)

// exportMock extends the base mockPersistence with methods needed for export/import tests.
type exportMock struct {
	mockPersistence
	revisions        map[string]*api.Revision
	createdFlos      []api.Flo
	createdRevisions []api.Revision
	triggers         map[string][]*api.Trigger
}

func newExportMock() *exportMock {
	return &exportMock{
		mockPersistence: *newMockPersistence(),
		revisions:       make(map[string]*api.Revision),
		triggers:        make(map[string][]*api.Trigger),
	}
}

func (m *exportMock) GetLatestRevisionByFloID(floID string) (*api.Revision, error) {
	return m.revisions[floID], nil
}

func (m *exportMock) CreateFlo(flo api.Flo) (*string, error) {
	id := "imported-flo-1"
	flo.ID = id
	m.createdFlos = append(m.createdFlos, flo)
	m.flos[id] = &flo
	return &id, nil
}

func (m *exportMock) CreateFloRevision(rev api.Revision) (*string, error) {
	id := "imported-rev-1"
	m.createdRevisions = append(m.createdRevisions, rev)
	return &id, nil
}

func (m *exportMock) GetTriggersByFloID(floID string) ([]*api.Trigger, error) {
	return m.triggers[floID], nil
}

func (m *exportMock) CreateTriggerWithType(t api.Trigger) (*string, error) {
	id := "imported-trigger-1"
	return &id, nil
}

func (m *exportMock) LinkFloToTrigger(floID string, triggerID string) error {
	return nil
}

func (m *exportMock) GetActions() ([]*api.Action, error) {
	return nil, nil
}

func setupExportRouter(svc *Service) *gin.Engine {
	router := gin.New()
	flos := router.Group("/flo")
	flos.POST("/export", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	}, svc.exportFlos)
	flos.POST("/import", func(c *gin.Context) {
		c.Set("account_id", "user-1")
		c.Next()
	}, svc.importFlo)
	return router
}

func Test_ExportFlos_ValidIDs_ReturnsFlows(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newExportMock()
	mock.flos["flo-1"] = &api.Flo{ID: "flo-1", Name: "Test Flow"}
	mock.users["user-1"] = &api.User{ID: "user-1"}
	mock.revisions["flo-1"] = &api.Revision{
		ID:    "rev-1",
		FloID: "flo-1",
		Data:  []byte(`{"nodes":[],"edges":[]}`),
	}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock // use the export mock which overrides GetLatestRevisionByFloID
	router := setupExportRouter(svc)

	body, _ := json.Marshal(map[string]interface{}{"ids": []string{"flo-1"}})
	req := httptest.NewRequest(http.MethodPost, "/flo/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusOK))

	var result []map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result).To(HaveLen(1))
	Expect(result[0]["name"]).To(Equal("Test Flow"))
}

func Test_ExportFlos_NonExistentID_ReturnsNotFound(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newExportMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupExportRouter(svc)

	body, _ := json.Marshal(map[string]interface{}{"ids": []string{"nonexistent"}})
	req := httptest.NewRequest(http.MethodPost, "/flo/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusNotFound))
}

func Test_ExportFlos_EmptyIDs_ReturnsBadRequest(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newExportMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupExportRouter(svc)

	body, _ := json.Marshal(map[string]interface{}{"ids": []string{}})
	req := httptest.NewRequest(http.MethodPost, "/flo/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
}

func Test_ImportFlo_ValidWrapper_CreatesFlow(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newExportMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupExportRouter(svc)

	flowData := map[string]interface{}{
		"name":  "Imported Flow",
		"scale": 1.0,
		"x":     0,
		"y":     0,
		"revision": map[string]interface{}{
			"nodes": []interface{}{},
			"edges": []interface{}{},
		},
	}
	flowDataJSON, _ := json.Marshal(flowData)
	hash := sha256.Sum256(flowDataJSON)

	wrapper := map[string]interface{}{
		"flomation_export": map[string]interface{}{
			"version":          1,
			"exported_at":      "2026-03-24T12:00:00Z",
			"source_flow_id":   "original-id",
			"source_flow_name": "Original Flow",
			"author_email":     "test@example.com",
			"hash":             hex.EncodeToString(hash[:]),
		},
		"flow_data": flowData,
	}

	body, _ := json.Marshal(wrapper)
	req := httptest.NewRequest(http.MethodPost, "/flo/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusCreated))

	var result map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["imported"]).To(BeTrue())
	Expect(result["name"]).To(Equal("Imported Flow"))
	Expect(mock.createdFlos).To(HaveLen(1))
	Expect(mock.createdFlos[0].Name).To(Equal("Imported Flow"))
}

func Test_ImportFlo_TamperedHash_Rejected(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newExportMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupExportRouter(svc)

	flowData := map[string]interface{}{
		"name":  "Tampered Flow",
		"scale": 1.0,
		"x":     0,
		"y":     0,
	}

	wrapper := map[string]interface{}{
		"flomation_export": map[string]interface{}{
			"version":          1,
			"exported_at":      "2026-03-24T12:00:00Z",
			"source_flow_id":   "original-id",
			"source_flow_name": "Original Flow",
			"author_email":     "test@example.com",
			"hash":             "0000000000000000000000000000000000000000000000000000000000000000",
		},
		"flow_data": flowData,
	}

	body, _ := json.Marshal(wrapper)
	req := httptest.NewRequest(http.MethodPost, "/flo/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))

	var result map[string]interface{}
	Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
	Expect(result["error"]).To(ContainSubstring("hash verification failed"))
}

func Test_ImportFlo_MissingMetadata_Rejected(t *testing.T) {
	t.Parallel()
	RegisterTestingT(t)

	mock := newExportMock()
	mock.users["user-1"] = &api.User{ID: "user-1"}

	svc := setupTestService(&mock.mockPersistence)
	svc.persistence = mock
	router := setupExportRouter(svc)

	wrapper := map[string]interface{}{
		"flomation_export": map[string]interface{}{},
		"flow_data": map[string]interface{}{
			"name": "Test",
		},
	}

	body, _ := json.Marshal(wrapper)
	req := httptest.NewRequest(http.MethodPost, "/flo/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	Expect(w.Code).To(Equal(http.StatusBadRequest))
}

// Phase 5 stubs
func (m *exportMock) GetAgentIdentitiesByUserID(agentUserID string) ([]*api.AgentIdentity, error) { return nil, nil }
func (m *exportMock) LookupIdentity(agentID, channelType, externalID string) (*api.AgentIdentity, *api.AgentUser, error) { return nil, nil, nil }
func (m *exportMock) MergeAgentUsers(agentID, sourceUserID, targetUserID string) error { return nil }
func (m *exportMock) GetPendingActionByUserAndType(agentUserID, actionType string) (*api.AgentPendingAction, error) { return nil, nil }

// Phase 4 stubs
func (m *exportMock) SearchMemoriesByEmbedding(agentID, agentUserID string, embedding pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *exportMock) GetMemoriesWithoutEmbedding(limit int) ([]*api.AgentMemory, error) {
	return nil, nil
}
func (m *exportMock) UpdateMemoryEmbedding(id string, embedding pgvector.Vector) error {
	return nil
}

// Phase 6 stubs
func (m *exportMock) GetAgentUserByEmail(agentID, email string) (*api.AgentUser, error) { return nil, nil }
func (m *exportMock) GetAgentUsersByAgentID(agentID string, limit, offset int) ([]*api.AgentUser, error) { return nil, nil }
func (m *exportMock) UpdateAgentMemory(id, title, body string, pinned bool) error { return nil }
func (m *exportMock) DeleteAllMemoriesForUser(agentUserID string) (int64, error) { return 0, nil }
func (m *exportMock) GetExpiredMemories(limit int) ([]*api.AgentMemory, error) { return nil, nil }
func (m *exportMock) DeleteMemoriesOlderThan(agentID string, olderThan time.Time, excludePinned bool) (int64, error) { return 0, nil }
func (m *exportMock) DeleteExpiredMemories(limit int) (int64, error) { return 0, nil }
func (m *exportMock) GetAgentsWithRetentionPolicy() ([]struct{ ID string `db:"id"`; MemoryRetentionDays int `db:"memory_retention_days"` }, error) { return nil, nil }
func (m *exportMock) UpdateAgentRetentionDays(agentID string, days *int) error { return nil }
func (m *exportMock) CreateAuditLogEntry(entry api.AgentAuditLog) (*string, error) { return nil, nil }
func (m *exportMock) GetAuditLogForAgent(agentID string, limit, offset int) ([]*api.AgentAuditLog, error) { return nil, nil }
func (m *exportMock) GetAuditLogForUser(agentUserID string, limit, offset int) ([]*api.AgentAuditLog, error) { return nil, nil }
func (m *exportMock) UnlinkAgentIdentity(identityID string) error { return nil }
func (m *exportMock) GetAllDataForUser(agentUserID string) (*api.AgentDataExport, error) { return nil, nil }
