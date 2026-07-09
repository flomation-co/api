package http

import (
	"encoding/json"
	"errors"
	"io"
	gohttp "net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// sendgridOptionHosts maps the node's Region input to the fixed SendGrid REST
// host the option proxies call. The hosts are constants (never
// caller-supplied), so — like the Intercom proxies — there is NO SSRF surface
// here and the client needs no dial Control / metadata-IP guard. Kept in sync
// with the executor's sendgrid common host map. The empty key is the Global
// default; "eu" is the EU data-residency host, which carries no Marketing
// endpoints — the lists/segments proxies surface SendGrid's own error there.
var sendgridOptionHosts = map[string]string{
	"":   "https://api.sendgrid.com",
	"eu": "https://api.eu.sendgrid.com",
}

// sendgridMaxOptionPages bounds the edit-time fetch when following
// _metadata.next pagination — beyond it the picker lists what was fetched and
// operators can still type an id manually.
const sendgridMaxOptionPages = 10

// sendgridOptionsHTTPClient is shared across the SendGrid dropdown proxies so
// connections to the fixed API hosts are pooled. Cross-host redirects are
// refused defensively.
var sendgridOptionsHTTPClient = &gohttp.Client{
	Timeout: 15 * time.Second,
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

// getSendGridLists serves the account's marketing contact lists for the List
// pickers.
func (s *Service) getSendGridLists(c *gin.Context) {
	q := url.Values{}
	q.Set("page_size", "1000")
	s.serveSendGridNamed(c, "/v3/marketing/lists", q, []string{"result"})
}

// getSendGridTemplates serves the account's dynamic transactional templates
// for the Template pickers. The endpoint defaults to legacy generations and
// requires page_size, so both are always sent; the envelope key is "result"
// with "templates" accepted defensively.
func (s *Service) getSendGridTemplates(c *gin.Context) {
	q := url.Values{}
	q.Set("generations", "dynamic")
	q.Set("page_size", "200")
	s.serveSendGridNamed(c, "/v3/templates", q, []string{"result", "templates"})
}

// getSendGridSegments serves the account's marketing segments for the Segment
// picker. The 2.0 endpoint returns every segment in one page (no pagination).
func (s *Service) getSendGridSegments(c *gin.Context) {
	s.serveSendGridNamed(c, "/v3/marketing/segments/2.0", nil, []string{"results"})
}

// getSendGridAsmGroups serves the account's unsubscribe (ASM) groups. The
// response is a bare top-level array and the ids are numbers; the id is shown
// alongside the name because mail send's asm_group_id is the numeric id
// operators otherwise have to look up.
func (s *Service) getSendGridAsmGroups(c *gin.Context) {
	auth, ok := s.resolveSendGridAuth(c)
	if !ok {
		return
	}
	body, errMsg := fetchSendGridPage(c, auth, "/v3/asm/groups", nil)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(body, &rows); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the SendGrid response"})
		return
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		id := sendgridStr(r["id"])
		if id == "" {
			continue
		}
		name := strings.TrimSpace(sendgridStr(r["name"]))
		if name == "" {
			name = id
		} else {
			name += " (" + id + ")"
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	writeSendGridOptions(c, options)
}

// serveSendGridNamed is the common path for the enveloped list endpoints:
// resolve auth, fetch the named array (following _metadata.next), map the
// rows' name/id to options and write.
func (s *Service) serveSendGridNamed(c *gin.Context, path string, query url.Values, arrayKeys []string) {
	auth, ok := s.resolveSendGridAuth(c)
	if !ok {
		return
	}
	rows, errMsg := fetchSendGridRows(c, auth, path, query, arrayKeys)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		id := sendgridStr(r["id"])
		if id == "" {
			continue
		}
		name := strings.TrimSpace(sendgridStr(r["name"]))
		if name == "" {
			name = id
		}
		options = append(options, api.InputOption{Name: name, Value: id})
	}
	writeSendGridOptions(c, options)
}

// sendgridAuth is the resolved connection the proxies call SendGrid with: the
// fixed Global/EU host plus the plaintext API key.
type sendgridAuth struct {
	host string
	key  string
}

// resolveSendGridAuth pulls the node's Region and API Key from the query
// params. The region selects a fixed host (empty/unknown/unresolved values
// fall back to Global); the key is a secret that may arrive as a ${secrets.X}
// reference resolved server-side behind the EnvironmentView permission gate.
// On any problem it writes the option-proxy error response (HTTP 200 +
// {"error": …}) and returns ok=false so the editor shows the message inline
// and falls back to manual entry.
func (s *Service) resolveSendGridAuth(c *gin.Context) (sendgridAuth, bool) {
	region := strings.ToLower(strings.TrimSpace(c.Query("region")))
	host, ok := sendgridOptionHosts[region]
	if !ok {
		host = sendgridOptionHosts[""]
	}

	key := strings.TrimSpace(c.Query("api_key"))
	if strings.HasPrefix(key, "${credentials.") || strings.HasPrefix(key, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use an environment secret for the API Key (the flow itself still runs)"})
		return sendgridAuth{}, false
	}
	if strings.HasPrefix(key, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the API Key secret"})
			return sendgridAuth{}, false
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return sendgridAuth{}, false // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, key)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return sendgridAuth{}, false
		}
		key = resolved
	}
	if key == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the API Key to load this list"})
		return sendgridAuth{}, false
	}
	return sendgridAuth{host: host, key: key}, true
}

// fetchSendGridRows fetches the named top-level array from an enveloped
// SendGrid endpoint, following _metadata.next pagination up to
// sendgridMaxOptionPages. The next link is never dialled directly — only its
// query string is reused against OUR fixed host and path, so a hostile next
// URL can't point the proxy elsewhere.
func fetchSendGridRows(c *gin.Context, auth sendgridAuth, path string, query url.Values, arrayKeys []string) ([]map[string]interface{}, string) {
	var rows []map[string]interface{}
	for page := 0; page < sendgridMaxOptionPages; page++ {
		body, errMsg := fetchSendGridPage(c, auth, path, query)
		if errMsg != "" {
			return nil, errMsg
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, "Failed to parse the SendGrid response"
		}
		var raw json.RawMessage
		for _, key := range arrayKeys {
			if sendgridIsArray(env[key]) {
				raw = env[key]
				break
			}
		}
		if raw == nil {
			return nil, "Failed to parse the SendGrid response"
		}
		var pageRows []map[string]interface{}
		if err := json.Unmarshal(raw, &pageRows); err != nil {
			return nil, "Failed to parse the SendGrid response"
		}
		rows = append(rows, pageRows...)
		if len(pageRows) == 0 {
			break
		}
		next := sendgridNextQuery(env)
		if next == nil {
			break
		}
		query = next
	}
	return rows, ""
}

// fetchSendGridPage performs one Bearer-authenticated GET against the fixed
// host and returns the raw response body.
func fetchSendGridPage(c *gin.Context, auth sendgridAuth, path string, query url.Values) ([]byte, string) {
	reqURL := auth.host + path
	if enc := query.Encode(); enc != "" {
		reqURL += "?" + enc
	}
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "Could not build the SendGrid request"
	}
	req.Header.Set("Authorization", "Bearer "+auth.key)
	req.Header.Set("Accept", "application/json")

	resp, err := sendgridOptionsHTTPClient.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to reach SendGrid for options")
		return nil, "Could not reach SendGrid — check your connection and try again"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
		return nil, "SendGrid rejected the request as unauthorised — check the API Key and Region"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "SendGrid returned an unexpected response (HTTP " + strconv.Itoa(resp.StatusCode) + ")"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "Failed to read the SendGrid response"
	}
	return body, ""
}

// sendgridNextQuery extracts the query string from _metadata.next so the next
// page is re-issued against the fixed host+path. Returns nil on the last page.
func sendgridNextQuery(env map[string]json.RawMessage) url.Values {
	var meta struct {
		Next string `json:"next"`
	}
	if raw, ok := env["_metadata"]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	if meta.Next == "" {
		return nil
	}
	u, err := url.Parse(meta.Next)
	if err != nil {
		return nil
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) == 0 {
		return nil
	}
	return q
}

// sendgridIsArray reports whether a raw JSON value is an array (SendGrid
// envelopes mix arrays with scalar/object siblings like _metadata and
// contact_count).
func sendgridIsArray(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "[")
}

// writeSendGridOptions sorts the options by name and writes the option-proxy
// success envelope.
func writeSendGridOptions(c *gin.Context, options []api.InputOption) {
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// sendgridStr renders an id/label field that may arrive as a JSON string or
// number (list/template/segment ids are strings, ASM group ids are numbers) as
// a plain string.
func sendgridStr(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}
