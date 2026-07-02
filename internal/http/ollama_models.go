package http

import (
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

// ollamaModelsHTTPClient is shared across calls so connections to users'
// Ollama servers are pooled. The timeout is short: /api/tags is a local
// metadata read, and the editor is waiting on this to render a dropdown.
//
// Unlike the other option proxies the upstream host is caller-supplied, so
// the dialer refuses link-local destinations (cloud metadata services live
// at 169.254.169.254). The check runs on the address actually being dialed,
// which also covers DNS-based tricks. Loopback and private LAN ranges stay
// allowed — reaching the user's own Ollama box is the point of the feature,
// and flows already make executor-side requests to the same ranges.
var ollamaModelsHTTPClient = &gohttp.Client{
	Timeout: 10 * time.Second,
	Transport: &gohttp.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				if ip := net.ParseIP(host); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
					return errors.New("link-local addresses are not allowed")
				}
				return nil
			},
		}).DialContext,
	},
}

// getOllamaModels serves the installed-model list from a user's own Ollama
// server as dropdown options for the ai/ollama action's model input.
// Unlike the OpenRouter proxy there is no fixed upstream: the server URL is
// per-node configuration, so the editor forwards the node's endpoint and
// api_key inputs as query parameters (declared via the marker's Params in
// dynamicOptionsMetadata). Secret-typed api_key values arrive as
// ${secrets.X} references and are resolved server-side; the plaintext never
// transits the browser. There is no cache — different requests target
// different servers, and a local /api/tags responds in milliseconds.
// Errors follow the option-proxy convention of HTTP 200 + {"error": ...} so
// the editor shows the message inline and falls back to the static options.
func (s *Service) getOllamaModels(c *gin.Context) {
	endpoint := strings.TrimSpace(c.Query("endpoint"))
	if endpoint == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Ollama Server URL to load its model list"})
		return
	}
	upstreamURL, err := tagsURL(endpoint)
	if err != nil {
		// Also the path taken when the endpoint input holds an unresolved
		// ${...} reference — the static options remain in play.
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Ollama Server URL must be a full http(s) URL"})
		return
	}

	apiKey := strings.TrimSpace(c.Query("api_key"))
	if strings.HasPrefix(apiKey, "${credentials.") || strings.HasPrefix(apiKey, "${credential.") {
		// Secret slots also accept managed-credential references, but this
		// resolver only handles environment secrets — say so rather than
		// failing with a spurious secret-not-found.
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't be used to load the model list — use an environment secret for the API key (the flow itself still runs)"})
		return
	}
	if strings.HasPrefix(apiKey, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the API key secret"})
			return
		}
		// Resolving a secret to plaintext here must be gated by the same
		// permission as reading it through the environment endpoints: the
		// resolved value is sent as a Bearer header to a caller-supplied
		// host, so without this check a member denied environment.view
		// could exfiltrate any secret by aiming the endpoint at a server
		// they control.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, apiKey)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return
		}
		apiKey = resolved
	}

	options, err := fetchOllamaModels(c, upstreamURL, apiKey)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Warn("unable to fetch Ollama models")
		var statusErr *ollamaStatusError
		switch {
		case errors.As(err, &statusErr) && (statusErr.status == gohttp.StatusUnauthorized || statusErr.status == gohttp.StatusForbidden):
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Ollama server rejected the request as unauthorised — check the API key"})
		case errors.As(err, &statusErr):
			c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("The Ollama server returned an unexpected response (HTTP %d)", statusErr.status)})
		default:
			c.JSON(gohttp.StatusOK, gin.H{"error": "Could not reach the Ollama server — check the URL and that the server is running"})
		}
		return
	}
	if len(options) == 0 {
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Ollama server has no models installed — pull one first, e.g. `ollama pull llama3.2`"})
		return
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// tagsURL normalises a user-supplied endpoint into the native model-listing
// URL, mirroring the executor action's chatURL normaliser: base URLs, bare
// /api suffixes and pasted full paths are all accepted. It is built via
// url.URL rather than string concatenation so a crafted endpoint (e.g. a
// trailing "?") cannot displace the forced /api/tags path into the query
// string and aim the proxy at an arbitrary path.
func tagsURL(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("endpoint must be a full http(s) URL")
	}
	path := strings.TrimRight(parsed.Path, "/")
	path = strings.TrimSuffix(path, "/api/tags")
	path = strings.TrimSuffix(path, "/api/chat")
	path = strings.TrimSuffix(path, "/api")
	parsed.Path = path + "/api/tags"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), nil
}

func fetchOllamaModels(c *gin.Context, upstreamURL, apiKey string) ([]api.InputOption, error) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, upstreamURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := ollamaModelsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != gohttp.StatusOK {
		return nil, &ollamaStatusError{status: resp.StatusCode}
	}

	var upstream struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, err
	}

	// A tag is both the display name and the value — Ollama has no
	// separate human-readable title. An empty list is NOT an error here
	// (unlike the OpenRouter proxy): a freshly-installed server genuinely
	// has no models, and the caller turns that into a helpful message.
	options := make([]api.InputOption, 0, len(upstream.Models))
	for _, m := range upstream.Models {
		if m.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: m.Name, Value: m.Name})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})

	return options, nil
}

type ollamaStatusError struct {
	status int
}

func (e *ollamaStatusError) Error() string {
	return fmt.Sprintf("Ollama tags endpoint returned status %d %s", e.status, gohttp.StatusText(e.status))
}
