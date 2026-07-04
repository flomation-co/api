package http

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	gohttp "net/http"
	"net/url"
	"sort"
	"strings"
	"syscall"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// jenkinsOptionsHTTPClient is shared across calls so connections to users'
// Jenkins servers are pooled. The timeout is short: /api/json?tree=jobs[name]
// is a cheap metadata read and the editor is waiting on it to render a dropdown.
//
// As with the Ollama proxy the upstream host is caller-supplied, so the dialer
// refuses link-local destinations (cloud metadata services live at
// 169.254.169.254). The check runs on the address actually being dialed, which
// also covers DNS-based tricks. Loopback and private LAN ranges stay allowed —
// self-hosted Jenkins commonly lives on a private network, and flows already
// make executor-side requests to the same ranges.
var jenkinsOptionsHTTPClient = &gohttp.Client{
	Timeout: 10 * time.Second,
	// The dialer Control runs on every dialed address, including redirect
	// targets, so link-local/metadata IPs are blocked even mid-redirect. But a
	// redirect to a *different* host in an allowed (private) range would still
	// be followed, so CheckRedirect additionally refuses cross-host redirects —
	// the jobs endpoint returns JSON directly and never needs to leave the host.
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
					// Covers 169.254.169.254 (and its ::ffff: mapped form).
					return errors.New("link-local addresses are not allowed")
				}
				// Cloud instance-metadata endpoints that don't live in a
				// link-local range: AWS IMDS over IPv6, and the 100.100.100.200
				// address several clouds use. The check runs on the address
				// actually dialed, so a DNS name or a redirect resolving to one
				// of these is caught too.
				if isCloudMetadataIP(ip) {
					return errors.New("cloud metadata addresses are not allowed")
				}
				return nil
			},
		}).DialContext,
	},
}

// blockedMetadataIPs are instance-metadata service addresses outside the
// link-local range. fd00:ec2::254 is AWS's documented IPv6 IMDS endpoint;
// 100.100.100.200 is Alibaba Cloud's metadata address. Private RFC1918 / ULA
// ranges are deliberately NOT blocked — a self-hosted Jenkins legitimately
// lives there, and flows already reach those ranges executor-side.
var blockedMetadataIPs = []net.IP{
	net.ParseIP("fd00:ec2::254"),
	net.ParseIP("100.100.100.200"),
}

func isCloudMetadataIP(ip net.IP) bool {
	for _, b := range blockedMetadataIPs {
		if b != nil && ip.Equal(b) {
			return true
		}
	}
	return false
}

// getJenkinsJobs serves an instance's jobs as dropdown options for every
// Jenkins action's "Job" input. There is no fixed upstream: the server URL is
// per-node configuration, so the editor forwards the node's base_url / username
// / api_token inputs as query parameters (declared via the marker's Params in
// dynamicOptionsMetadata). Secret-typed api_token values arrive as ${secrets.X}
// references and are resolved server-side; the plaintext never transits the
// browser. Errors follow the option-proxy convention of HTTP 200 + {"error": …}
// so the editor shows the message inline and falls back to manual entry.
func (s *Service) getJenkinsJobs(c *gin.Context) {
	jobsURL, err := jenkinsJobsURL(strings.TrimSpace(c.Query("base_url")))
	if err != nil {
		// Also the path taken when base_url holds an unresolved ${...} reference.
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Jenkins URL (a full http(s) URL) to load the job list"})
		return
	}

	username := strings.TrimSpace(c.Query("username"))
	if username == "" || strings.HasPrefix(username, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Username to load the job list"})
		return
	}

	apiToken := strings.TrimSpace(c.Query("api_token"))
	if strings.HasPrefix(apiToken, "${credentials.") || strings.HasPrefix(apiToken, "${credential.") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load the job list — use an environment secret for the API token (the flow itself still runs)"})
		return
	}
	if strings.HasPrefix(apiToken, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the API token secret"})
			return
		}
		// Resolving a secret to plaintext here must be gated by the same
		// permission as reading it through the environment endpoints: the
		// resolved value authenticates a request to a caller-supplied host, so
		// without this check a member denied environment.view could exfiltrate
		// any secret by aiming the proxy at a server they control.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, apiToken)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return
		}
		apiToken = resolved
	}
	if apiToken == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the API Token to load the job list"})
		return
	}

	options, err := fetchJenkinsJobs(c, jobsURL, username, apiToken)
	if err != nil {
		log.WithField("error", err).Warn("unable to fetch Jenkins jobs")
		var statusErr *jenkinsStatusError
		switch {
		case errors.As(err, &statusErr) && (statusErr.status == gohttp.StatusUnauthorized || statusErr.status == gohttp.StatusForbidden):
			c.JSON(gohttp.StatusOK, gin.H{"error": "Jenkins rejected the request as unauthorised — check the Username and API Token"})
		case errors.As(err, &statusErr):
			c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("Jenkins returned an unexpected response (HTTP %d)", statusErr.status)})
		default:
			c.JSON(gohttp.StatusOK, gin.H{"error": "Could not reach Jenkins — check the Jenkins URL and that the server is running"})
		}
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// jenkinsJobsURL turns a user-supplied instance URL into the jobs-listing URL.
// It is built via url.URL rather than string concatenation so a crafted base
// (e.g. a trailing "?") cannot displace the forced /api/json path into the
// query string. A bare host is tolerated (https assumed); a context path
// (https://host/jenkins) is preserved.
func jenkinsJobsURL(base string) (string, error) {
	if base == "" {
		return "", errors.New("base_url is required")
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errors.New("base_url must be a full http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("base_url must be http or https")
	}
	// Drop any userinfo (user:pass@host) so a pasted URL can't smuggle
	// credentials into the server-side request.
	u.User = nil
	u.Path = strings.TrimRight(u.Path, "/") + "/api/json"
	u.RawPath = ""
	u.Fragment = ""
	u.ForceQuery = false
	u.RawQuery = url.Values{"tree": {"jobs[name,url,color]"}}.Encode()
	return u.String(), nil
}

func fetchJenkinsJobs(c *gin.Context, jobsURL, username, apiToken string) ([]api.InputOption, error) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, jobsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+apiToken)))
	req.Header.Set("Accept", "application/json")

	resp, err := jenkinsOptionsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != gohttp.StatusOK {
		return nil, &jenkinsStatusError{status: resp.StatusCode}
	}

	var upstream struct {
		Jobs []struct {
			Name string `json:"name"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, err
	}

	options := make([]api.InputOption, 0, len(upstream.Jobs))
	for _, j := range upstream.Jobs {
		if j.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: j.Name, Value: j.Name})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	return options, nil
}

type jenkinsStatusError struct {
	status int
}

func (e *jenkinsStatusError) Error() string {
	return fmt.Sprintf("Jenkins jobs endpoint returned status %d %s", e.status, gohttp.StatusText(e.status))
}
