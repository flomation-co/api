package http

import (
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

// woocommerceOptionsHTTPClient is shared across the WooCommerce dropdown proxies
// so connections to stores are pooled. The timeout is short: the editor waits on
// this to render a dropdown.
//
// As with the Jenkins/Ollama proxies the upstream host is caller-supplied, so
// the dialer refuses link-local and cloud-metadata destinations (169.254.169.254
// et al) on the address actually dialed — which also covers DNS/redirect tricks.
// Loopback and private LAN ranges stay allowed: self-hosted WooCommerce commonly
// lives on a private network, and flows already reach those ranges executor-side.
var woocommerceOptionsHTTPClient = &gohttp.Client{
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
	Transport: &gohttp.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
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
			},
		}).DialContext,
	},
}

// getWooCommerceCategories serves the store's product categories as dropdown
// options for the product "category" filter input.
func (s *Service) getWooCommerceCategories(c *gin.Context) {
	s.serveWooCommerceOptions(c, "/products/categories")
}

// getWooCommerceTags serves the store's product tags as dropdown options for the
// product "tag" filter input.
func (s *Service) getWooCommerceTags(c *gin.Context) {
	s.serveWooCommerceOptions(c, "/products/tags")
}

// serveWooCommerceOptions resolves the node's WooCommerce connection from the
// query params (store url plain; consumer_key/secret as secret references
// resolved server-side), calls the store's REST API, and returns the taxonomy as
// {name, value} options. Errors follow the option-proxy convention of HTTP 200 +
// {"error": …} so the editor shows the message inline and falls back to manual
// entry.
func (s *Service) serveWooCommerceOptions(c *gin.Context, path string) {
	base, err := woocommerceOptionsBaseURL(strings.TrimSpace(c.Query("url")))
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Store URL (a full http(s) URL) to load this list"})
		return
	}

	key := strings.TrimSpace(c.Query("consumer_key"))
	secret := strings.TrimSpace(c.Query("consumer_secret"))

	// Managed credentials can't be resolved to a plaintext key pair here.
	for _, v := range []string{key, secret} {
		if strings.HasPrefix(v, "${credentials.") || strings.HasPrefix(v, "${credential.") {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load this list — use environment secrets for the Consumer Key/Secret (the flow itself still runs)"})
			return
		}
	}

	// Either part may be a ${secrets.X} reference. Resolve them server-side,
	// gating the plaintext resolution on the same permission as reading a secret
	// through the environment endpoints (the resolved values authenticate a
	// request to a caller-supplied host).
	if strings.HasPrefix(key, "${") || strings.HasPrefix(secret, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the Consumer Key/Secret"})
			return
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return // checkPermission has written the response
		}
		if strings.HasPrefix(key, "${") {
			resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, key)
			if errMsg != "" {
				c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
				return
			}
			key = resolved
		}
		if strings.HasPrefix(secret, "${") {
			resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, secret)
			if errMsg != "" {
				c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
				return
			}
			secret = resolved
		}
	}
	if key == "" || secret == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Consumer Key and Consumer Secret to load this list"})
		return
	}

	inQuery := strings.EqualFold(strings.TrimSpace(c.Query("credentials_in_query")), "true")

	options, errMsg := s.fetchWooCommerceOptions(c, base, key, secret, inQuery, path)
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// woocommerceOptionsBaseURL turns a user-supplied store URL into a clean
// scheme+host[+path] base (no trailing slash, no REST-API suffix), defaulting to
// https. Built via url.URL so a crafted base can't smuggle userinfo or a query
// into the server-side request.
func woocommerceOptionsBaseURL(raw string) (string, error) {
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
	for _, suffix := range []string{"/wp-json/wc/v3", "/wp-json/wc/v2", "/wp-json/wc/v1", "/wp-json"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			break
		}
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// maxWooOptionPages caps how many pages the WooCommerce dropdown proxies follow
// (100 taxonomy terms/page). A handful of pages covers realistic category/tag
// counts while bounding an edit-time fetch; beyond it the list is truncated (the
// picker still falls back to manual entry).
const maxWooOptionPages = 10

type wooNamedTerm struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// redactWooOptionCreds scrubs the consumer key/secret (raw and URL-escaped) from
// a message. In credentials-in-query mode they appear in the request URL, which
// the net/http transport echoes into its errors; this keeps them out of logs.
func redactWooOptionCreds(msg, key, secret string) string {
	for _, s := range []string{secret, key} {
		if s == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, url.QueryEscape(s), "REDACTED")
		msg = strings.ReplaceAll(msg, s, "REDACTED")
	}
	return msg
}

func (s *Service) fetchWooCommerceOptions(c *gin.Context, base, key, secret string, inQuery bool, path string) ([]api.InputOption, string) {
	var rows []wooNamedTerm
	for page := 1; page <= maxWooOptionPages; page++ {
		endpoint := base + "/wp-json/wc/v3" + path
		q := url.Values{}
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		if inQuery {
			q.Set("consumer_key", key)
			q.Set("consumer_secret", secret)
		}
		reqURL := endpoint + "?" + q.Encode()

		req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, reqURL, nil)
		if err != nil {
			return nil, "Could not build the WooCommerce request"
		}
		if !inQuery {
			req.SetBasicAuth(key, secret)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := woocommerceOptionsHTTPClient.Do(req)
		if err != nil {
			// In credentials-in-query mode reqURL carries the key pair, and the
			// transport's *url.Error echoes it — scrub before logging so a secret
			// can't leak into application logs.
			log.WithField("error", redactWooOptionCreds(err.Error(), key, secret)).Warn("unable to reach WooCommerce for options")
			return nil, "Could not reach WooCommerce — check the Store URL and that the store is reachable"
		}
		if resp.StatusCode == gohttp.StatusUnauthorized || resp.StatusCode == gohttp.StatusForbidden {
			_ = resp.Body.Close()
			return nil, "WooCommerce rejected the request as unauthorised — check the Consumer Key and Secret"
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			code := resp.StatusCode
			_ = resp.Body.Close()
			return nil, "WooCommerce returned an unexpected response (HTTP " + strconv.Itoa(code) + ")"
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		totalPages := resp.Header.Get("X-WP-TotalPages")
		_ = resp.Body.Close()
		if err != nil {
			return nil, "Failed to read the WooCommerce response"
		}

		var pageRows []wooNamedTerm
		if err := json.Unmarshal(body, &pageRows); err != nil {
			return nil, "Failed to parse the WooCommerce response"
		}
		rows = append(rows, pageRows...)

		// Stop when we've reached the last page (or the store didn't report one).
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
