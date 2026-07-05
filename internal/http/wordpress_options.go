package http

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	gohttp "net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// wordpressOptionsDialControl blocks link-local + cloud-metadata destinations on
// the address actually dialed (the WordPress site URL is caller-supplied). Same
// SSRF hardening as the Jenkins/WooCommerce proxies; loopback and private LAN
// stay allowed for self-hosted sites. Shared by the secure and insecure clients.
func wordpressOptionsDialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errors.New("link-local addresses are not allowed")
	}
	if isCloudMetadataIP(ip) {
		return errors.New("cloud metadata addresses are not allowed")
	}
	return nil
}

func wordpressOptionsRedirect(req *gohttp.Request, via []*gohttp.Request) error {
	if len(via) >= 5 {
		return errors.New("stopped after too many redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		return errors.New("cross-host redirect not allowed")
	}
	return nil
}

// wordpressOptionsHTTPClient / wordpressOptionsInsecureHTTPClient serve the
// dropdown proxies. Both are SSRF-hardened; the insecure one additionally skips
// TLS verification, used only when the node opted into "allow insecure SSL"
// (self-signed self-hosted sites) — kept separate so the secure default can
// never be weakened.
var wordpressOptionsHTTPClient = &gohttp.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: wordpressOptionsRedirect,
	Transport: &gohttp.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, Control: wordpressOptionsDialControl}).DialContext,
	},
}

var wordpressOptionsInsecureHTTPClient = &gohttp.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: wordpressOptionsRedirect,
	Transport: &gohttp.Transport{
		DialContext:     (&net.Dialer{Timeout: 5 * time.Second, Control: wordpressOptionsDialControl}).DialContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 — opt-in only
	},
}

func wpOptionsClient(insecure bool) *gohttp.Client {
	if insecure {
		return wordpressOptionsInsecureHTTPClient
	}
	return wordpressOptionsHTTPClient
}

// getWordPressCategories / getWordPressTags / getWordPressAuthors serve the
// site's categories, tags and authors as dropdown options for the post/page
// filters and author pickers.
func (s *Service) getWordPressCategories(c *gin.Context) {
	s.serveWordPressOptions(c, "/categories", nil)
}
func (s *Service) getWordPressTags(c *gin.Context) {
	s.serveWordPressOptions(c, "/tags", nil)
}
func (s *Service) getWordPressAuthors(c *gin.Context) {
	s.serveWordPressOptions(c, "/users", url.Values{"who": {"authors"}})
}

// serveWordPressOptions resolves the node's WordPress connection from the query
// params (site url + username plain; Application Password as a secret reference
// resolved server-side), calls the site's REST API with Basic auth, and returns
// the named array as {name, value} options. Errors follow the option-proxy
// convention of HTTP 200 + {"error": …} so the editor shows the message inline
// and falls back to manual entry.
func (s *Service) serveWordPressOptions(c *gin.Context, path string, extra url.Values) {
	base, err := wordpressOptionsBaseURL(strings.TrimSpace(c.Query("url")))
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Site URL (a full http(s) URL) to load this list"})
		return
	}

	username := strings.TrimSpace(c.Query("username"))
	if username == "" || strings.HasPrefix(username, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Username to load this list"})
		return
	}

	appPassword := strings.TrimSpace(c.Query("app_password"))
	if strings.HasPrefix(appPassword, "${credentials.") || strings.HasPrefix(appPassword, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use an environment secret for the Application Password (the flow itself still runs)"})
		return
	}
	if strings.HasPrefix(appPassword, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the Application Password secret"})
			return
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, appPassword)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return
		}
		appPassword = resolved
	}
	if appPassword == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Application Password to load this list"})
		return
	}

	insecure := strings.EqualFold(strings.TrimSpace(c.Query("allow_insecure")), "true")

	options, errMsg := s.fetchWordPressOptions(c, base, username, appPassword, insecure, path, extra)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// wordpressOptionsBaseURL turns a user-supplied site URL into a clean
// scheme+host[+path] base (no trailing slash, no REST-API suffix), defaulting to
// https. Built via url.URL so a crafted base can't smuggle userinfo or a query
// into the server-side request.
//
// NOTE: this normalisation mirrors the executor's NormaliseBaseURL (separate Go
// modules, no shared package). Keep them in sync.
func wordpressOptionsBaseURL(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "${") {
		return "", errors.New("url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("url must be a full http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("url must be http or https")
	}
	u.User = nil
	path := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/wp-json/wp/v2", "/wp-json/wp/v1", "/wp-json"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			break
		}
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// maxWpOptionPages caps how many pages the WordPress dropdown proxies follow
// (100 terms/users per page). A handful of pages covers realistic counts while
// bounding an edit-time fetch; beyond it the list is truncated (the picker still
// falls back to manual entry).
const maxWpOptionPages = 10

type wpNamedItem struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (s *Service) fetchWordPressOptions(c *gin.Context, base, username, appPassword string, insecure bool, path string, extra url.Values) ([]api.InputOption, string) {
	var rows []wpNamedItem
	for page := 1; page <= maxWpOptionPages; page++ {
		endpoint := base + "/wp-json/wp/v2" + path
		q := url.Values{}
		for k, vs := range extra {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		reqURL := endpoint + "?" + q.Encode()

		req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, reqURL, nil)
		if err != nil {
			return nil, "Could not build the WordPress request"
		}
		req.SetBasicAuth(username, appPassword)
		req.Header.Set("Accept", "application/json")

		resp, err := wpOptionsClient(insecure).Do(req)
		if err != nil {
			log.WithField("error", err).Warn("unable to reach WordPress for options")
			return nil, "Could not reach WordPress — check the Site URL and that the site is reachable"
		}
		if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
			_ = resp.Body.Close()
			return nil, "WordPress rejected the request as unauthorised — check the Username and Application Password"
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			code := resp.StatusCode
			_ = resp.Body.Close()
			return nil, "WordPress returned an unexpected response (HTTP " + strconv.Itoa(code) + ")"
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		totalPages := resp.Header.Get("X-WP-TotalPages")
		_ = resp.Body.Close()
		if err != nil {
			return nil, "Failed to read the WordPress response"
		}

		var pageRows []wpNamedItem
		if err := json.Unmarshal(body, &pageRows); err != nil {
			return nil, "Failed to parse the WordPress response"
		}
		rows = append(rows, pageRows...)

		if tp, perr := strconv.Atoi(totalPages); perr != nil || page >= tp {
			break
		}
	}

	options := make([]api.InputOption, 0, len(rows))
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: r.Name, Value: strconv.FormatInt(r.ID, 10)})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	return options, ""
}
