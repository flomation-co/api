package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	gohttp "net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// mondayAPIBase is the fixed Monday.com GraphQL endpoint the option proxies call.
// The host is a constant (never caller-supplied), so — like the Trello/Asana
// proxies — there is NO SSRF surface and the client needs no dial Control /
// metadata-IP guard. Kept in sync with the executor's monday_common.APIBase.
const mondayAPIBase = "https://api.monday.com/v2"
const mondayAPIVersion = "2023-10"

var mondayOptionsHTTPClient = &gohttp.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *gohttp.Request, via []*gohttp.Request) error {
		if len(via) >= 5 {
			return errors.New("stopped after too many redirects")
		}
		if req.URL.Host != via[0].URL.Host {
			return errors.New("cross-host redirect not allowed")
		}
		return nil
	},
}

// getMondayBoards serves the token's boards as dropdown options. No dependency.
func (s *Service) getMondayBoards(c *gin.Context) {
	token, ok := s.resolveMondayToken(c)
	if !ok {
		return
	}
	body, errMsg := s.doMondayQuery(c, token, `query { boards (limit: 200) { id name } }`, nil)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options, errMsg := mondayNamedOptions(body, "boards", "id", "name")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getMondayWorkspaces serves the token's workspaces as dropdown options.
func (s *Service) getMondayWorkspaces(c *gin.Context) {
	token, ok := s.resolveMondayToken(c)
	if !ok {
		return
	}
	body, errMsg := s.doMondayQuery(c, token, `query { workspaces (limit: 200) { id name } }`, nil)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options, errMsg := mondayNamedOptions(body, "workspaces", "id", "name")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getMondayGroups serves the selected board's groups. Depends on "board_id".
func (s *Service) getMondayGroups(c *gin.Context) {
	token, ok := s.resolveMondayToken(c)
	if !ok {
		return
	}
	boardID, ok := mondayRequireDependency(c, "board_id", "Select a Board to load its groups")
	if !ok {
		return
	}
	body, errMsg := s.doMondayQuery(c, token, `query ($boardId: [ID!]) { boards (ids: $boardId) { groups { id title } } }`,
		map[string]interface{}{"boardId": []string{boardID}})
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options, errMsg := mondayNestedBoardOptions(body, "groups", "id", "title")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getMondayColumns serves the selected board's columns. Depends on "board_id".
func (s *Service) getMondayColumns(c *gin.Context) {
	token, ok := s.resolveMondayToken(c)
	if !ok {
		return
	}
	boardID, ok := mondayRequireDependency(c, "board_id", "Select a Board to load its columns")
	if !ok {
		return
	}
	body, errMsg := s.doMondayQuery(c, token, `query ($boardId: [ID!]) { boards (ids: $boardId) { columns { id title } } }`,
		map[string]interface{}{"boardId": []string{boardID}})
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options, errMsg := mondayNestedBoardOptions(body, "columns", "id", "title")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

func mondayRequireDependency(c *gin.Context, name, prompt string) (string, bool) {
	v := strings.TrimSpace(c.Query(name))
	if v == "" || strings.HasPrefix(v, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": prompt})
		return "", false
	}
	return v, true
}

// resolveMondayToken pulls the node's Monday API token from the query params. The
// token is a secret that may arrive as a ${secrets.X} reference resolved
// server-side behind the EnvironmentView permission gate. On any problem it
// writes the option-proxy error response (HTTP 200 + {"error": …}).
func (s *Service) resolveMondayToken(c *gin.Context) (string, bool) {
	token := strings.TrimSpace(c.Query("api_token"))
	if strings.HasPrefix(token, "${credentials.") || strings.HasPrefix(token, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use an environment secret for the API Token (the flow itself still runs)"})
		return "", false
	}
	if strings.HasPrefix(token, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the API Token secret"})
			return "", false
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return "", false
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, token)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return "", false
		}
		token = resolved
	}
	if token == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the API Token to load this list"})
		return "", false
	}
	return token, true
}

// doMondayQuery POSTs a GraphQL query to the fixed Monday host and returns the
// raw body, translating transport/HTTP/GraphQL errors into a friendly message.
func (s *Service) doMondayQuery(c *gin.Context, token, query string, variables map[string]interface{}) ([]byte, string) {
	payload := map[string]interface{}{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	b, _ := json.Marshal(payload)
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodPost, mondayAPIBase, bytes.NewReader(b))
	if err != nil {
		return nil, "Could not build the Monday.com request"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("API-Version", mondayAPIVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := mondayOptionsHTTPClient.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to reach Monday.com for options")
		return nil, "Could not reach Monday.com — check your connection and try again"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
		return nil, "Monday.com rejected the request as unauthorised — check the API Token"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "Failed to read the Monday.com response"
	}
	// GraphQL returns 200 with an errors array on failure.
	var probe struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &probe) == nil && len(probe.Errors) > 0 {
		return nil, "Monday.com error: " + probe.Errors[0].Message
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "Monday.com returned an unexpected response"
	}
	return body, ""
}

// mondayNamedOptions extracts data.<key>[] rows into sorted {name,value} options
// (used for top-level lists like boards / workspaces).
func mondayNamedOptions(body []byte, key, idField, nameField string) ([]api.InputOption, string) {
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "Failed to parse the Monday.com response"
	}
	return mondayRowsToOptions(env.Data[key], idField, nameField)
}

// mondayNestedBoardOptions extracts data.boards[0].<key>[] rows (groups/columns
// nested under a single board).
func mondayNestedBoardOptions(body []byte, key, idField, nameField string) ([]api.InputOption, string) {
	var env struct {
		Data struct {
			Boards []map[string]json.RawMessage `json:"boards"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "Failed to parse the Monday.com response"
	}
	if len(env.Data.Boards) == 0 {
		return []api.InputOption{}, ""
	}
	return mondayRowsToOptions(env.Data.Boards[0][key], idField, nameField)
}

func mondayRowsToOptions(raw json.RawMessage, idField, nameField string) ([]api.InputOption, string) {
	if len(raw) == 0 {
		return []api.InputOption{}, ""
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, "Failed to parse the Monday.com response"
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		id := mondayStr(r[idField])
		if id == "" {
			continue
		}
		name := strings.TrimSpace(mondayStr(r[nameField]))
		if name == "" {
			name = id
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	return options, ""
}

func mondayStr(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		// Monday ids are strings, but be defensive about numeric ids: render an
		// integer without a decimal point, else a plain float. (Avoid a
		// TrimRight-based trim, which mangles values like "10.0" → "1".)
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}
