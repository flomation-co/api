package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	gohttp "net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// openRouterModelsURL is a var, not a const, so tests can point it at an
// httptest server (same seam as the executor's openrouter action).
var openRouterModelsURL = "https://openrouter.ai/api/v1/models"

// openRouterModelsHTTPClient is shared across calls so connections to the
// upstream are pooled rather than re-dialled per fetch.
var openRouterModelsHTTPClient = &gohttp.Client{Timeout: 10 * time.Second}

// The catalogue changes a handful of times a day at most; an hour keeps
// the editor snappy without re-fetching the multi-megabyte upstream list
// on every property-panel open.
const openRouterModelsCacheTTL = 1 * time.Hour

// openRouterModelsCache is the package-level TTL cache for the slimmed
// model list — same mutex+expiry idiom as agent.ScheduleCache. On upstream
// failure the stale list is served rather than an error, so a flaky
// connection never empties the editor's dropdown.
var openRouterModelsCache struct {
	mu        sync.Mutex
	options   []api.InputOption
	expiresAt time.Time
}

// getOpenRouterModels serves the OpenRouter model catalogue as dropdown
// options for the ai/openrouter action's model input, proxied and cached
// from OpenRouter's public models endpoint (no upstream key required).
// Response shape matches InputDefinition.Options: {"options": [{name, value}]}.
// Errors follow the option-proxy convention of HTTP 200 + {"error": ...}
// so the editor renders the message inline.
func (s *Service) getOpenRouterModels(c *gin.Context) {
	openRouterModelsCache.mu.Lock()
	if time.Now().Before(openRouterModelsCache.expiresAt) {
		options := openRouterModelsCache.options
		openRouterModelsCache.mu.Unlock()
		c.JSON(gohttp.StatusOK, gin.H{"options": options})
		return
	}
	openRouterModelsCache.mu.Unlock()

	// The upstream fetch runs without the lock so concurrent requests
	// don't queue behind a slow provider. Two expired-cache requests may
	// race and both fetch; the second write is idempotent (same public
	// catalogue), which is cheaper than single-flight machinery here.
	options, err := fetchOpenRouterModels(c)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Warn("unable to fetch OpenRouter models")
		openRouterModelsCache.mu.Lock()
		stale := openRouterModelsCache.options
		openRouterModelsCache.mu.Unlock()
		if stale != nil {
			c.JSON(gohttp.StatusOK, gin.H{"options": stale})
			return
		}
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to fetch models from OpenRouter"})
		return
	}

	openRouterModelsCache.mu.Lock()
	openRouterModelsCache.options = options
	openRouterModelsCache.expiresAt = time.Now().Add(openRouterModelsCacheTTL)
	openRouterModelsCache.mu.Unlock()
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

func fetchOpenRouterModels(c *gin.Context) ([]api.InputOption, error) {
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, openRouterModelsURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := openRouterModelsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// The full catalogue with descriptions and pricing runs to several
	// megabytes; cap well above that but below anything pathological.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != gohttp.StatusOK {
		return nil, &openRouterStatusError{status: resp.StatusCode}
	}

	var upstream struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, err
	}

	options := make([]api.InputOption, 0, len(upstream.Data))
	for _, m := range upstream.Data {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		options = append(options, api.InputOption{Name: name, Value: m.ID})
	}
	if len(options) == 0 {
		// A 200 with no usable entries (incident page, schema drift) must
		// count as a failure — caching it would displace the stale list
		// here and the static-options fallback in the editor for a full
		// TTL. OpenRouter's real catalogue is never empty.
		return nil, errors.New("OpenRouter models response contained no models")
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})

	return options, nil
}

type openRouterStatusError struct {
	status int
}

func (e *openRouterStatusError) Error() string {
	// Include the numeric code: OpenRouter sits behind Cloudflare, whose
	// outage codes (520/522/524/530) have no IANA StatusText and would
	// otherwise log as an empty message.
	return fmt.Sprintf("OpenRouter models endpoint returned status %d %s", e.status, gohttp.StatusText(e.status))
}
