// Package emailoctopus is the outbound connector for syncing
// marketing-opt-in state to EmailOctopus.
//
// Three operations are exposed: Subscribe, Unsubscribe, UpdateContact.
// The connector is intentionally small — it doesn't model EO's full
// API surface, only the slice we need for "user opts in / out" flows.
// Anything richer should live on top of this rather than inside it.
//
// All calls are synchronous against EO's REST API. Higher layers
// (the profile/welcome endpoints, the retry poller) decide when to
// invoke and how to handle failures — typically fire-and-forget with
// a retry-on-poll pattern so EO's availability never blocks a user's
// own profile save.
//
// **API version**: this connector targets EmailOctopus v2, recognisable
// by the `eo_…` API key prefix. v2 differs from v1.6 in three ways
// that matter here: auth is via the Authorization: Bearer header
// (not an api_key body field), the base URL is api.emailoctopus.com
// (not emailoctopus.com/api/1.6), and contact status values are
// lowercase ("subscribed" not "SUBSCRIBED"). All three were wrong on
// the original v1.6-shaped implementation, producing the unhelpful
// "Parameters are missing or invalid" 400 from EO.
//
// Contact IDs in v2 are derived as MD5(lowercase(email)), same as
// v1.6 — used in the URL path for PUT/DELETE.
package emailoctopus

import (
	"bytes"
	"crypto/md5" // #nosec G501 -- EmailOctopus mandates MD5 for contact_id derivation; not a security primitive
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"flomation.app/automate/api/internal/config"

	log "github.com/sirupsen/logrus"
)

const baseURL = "https://api.emailoctopus.com"

// platformTag is attached to every contact this connector subscribes
// so EmailOctopus segmentation can distinguish platform-sourced
// opt-ins from any other signup path (direct EO embedded form, an
// import, a partner integration, etc.). Survives the Subscribe-then-
// UpdateContact fallback on the 409 path so a returning user who
// previously self-signed-up gets re-tagged once they opt in via the
// platform.
const platformTag = "flomation-platform"

// EmailOctopus v2 status values. Lowercase, unlike v1.6.
const (
	statusSubscribed = "subscribed"
)

// Connector talks to the EmailOctopus REST API. Holds an http.Client
// and the resolved config block. Nil-safe at call time when the
// EmailOctopus block is unconfigured — operations become no-ops with
// a debug log so local dev (no EO credentials) doesn't fail noisily.
type Connector struct {
	cfg    *config.EmailOctopusConfig
	client *http.Client
}

func NewConnector(cfg *config.Config) *Connector {
	return &Connector{
		cfg: cfg.EmailOctopus,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Configured returns true when the connector has enough config to
// actually call EmailOctopus. Lets callers cheaply early-return in
// local-dev environments without spreading nil checks across the
// codebase.
func (c *Connector) Configured() bool {
	return c != nil && c.cfg != nil && c.cfg.APIKey != "" && c.cfg.ListID != ""
}

// contactID derives the EmailOctopus contact identifier from an
// email address. EO documents this as MD5(lowercase(email)) for both
// v1.6 and v2; the hash has no security meaning here, just identifier
// derivation. The MD5 algorithm is mandated by EO's API contract —
// switching to SHA-256 would produce identifiers EmailOctopus does
// not recognise.
func contactID(email string) string {
	// #nosec G401 -- EmailOctopus mandates MD5 for contact_id derivation
	// nosemgrep: go.lang.security.audit.crypto.use_of_weak_crypto.use-of-md5
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// subscribePayload is the body shape for POST /lists/{id}/contacts in
// EmailOctopus v2. POST sets the initial tag set on a new contact, so
// Tags is a flat array of strings — "the tags this contact should
// have". Status is lowercase.
//
// EmailOctopus v2 takes auth in the Authorization header rather than
// the body, so unlike v1.6 the payload doesn't carry api_key.
type subscribePayload struct {
	EmailAddress string            `json:"email_address"`
	Fields       map[string]string `json:"fields,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Status       string            `json:"status"`
}

// updatePayload is the body shape for PUT /lists/{id}/contacts/{id}.
// Unlike POST, PUT *mutates* the existing tag set rather than
// replacing it, so Tags is an add/remove object map — `{name: true}`
// to add, `{name: false}` to remove. We only ever add (the
// platformTag), so the map is always single-key true.
type updatePayload struct {
	Fields map[string]string `json:"fields,omitempty"`
	Tags   map[string]bool   `json:"tags,omitempty"`
	Status string            `json:"status,omitempty"`
}

// Subscribe adds a contact to the configured list, or — if the contact
// already exists — updates them to SUBSCRIBED. EmailOctopus returns
// 409 for already-existing contacts, which we translate into an
// implicit Update call so the operation is idempotent.
//
// We deliberately don't push the user's display name to EmailOctopus.
// EO custom fields are list-specific (a list might have `FirstName`,
// or `first_name`, or none at all) and pushing a field that isn't
// defined on the list returns the unhelpful "Parameters are missing
// or invalid" 400. The platform identifies subscribers by email
// + the `flomation-platform` tag; display name stays in Flomation
// where it actually belongs. If a future use case needs name sync,
// add a connector method that takes a configurable field-name map
// rather than hard-coding one.
func (c *Connector) Subscribe(email, name string) error {
	if !c.Configured() {
		log.Debug("emailoctopus: not configured, skipping Subscribe")
		return nil
	}
	_ = name // intentionally unused — see docstring above

	body, err := json.Marshal(subscribePayload{
		EmailAddress: email,
		Tags:         []string{platformTag},
		Status:       statusSubscribed,
	})
	if err != nil {
		return fmt.Errorf("marshal subscribe payload: %w", err)
	}

	url := fmt.Sprintf("%s/lists/%s/contacts", baseURL, c.cfg.ListID)
	resp, err := c.send(http.MethodPost, url, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		return nil
	case resp.StatusCode == http.StatusConflict:
		// Contact already exists — fall through to Update so the
		// caller's intent ("ensure this person is subscribed") is
		// honoured regardless of prior state.
		return c.UpdateContact(email, name, true)
	default:
		return fmt.Errorf("subscribe: status %d: %s", resp.StatusCode, readBody(resp))
	}
}

// Unsubscribe removes a contact from the configured list. Per the
// design decision (delete-not-mark), we use DELETE rather than
// PUT-to-UNSUBSCRIBED, so EmailOctopus stops holding the email after
// opt-out. A 404 from EO is treated as success — the contact wasn't
// there, which is the state we wanted anyway.
func (c *Connector) Unsubscribe(email string) error {
	if !c.Configured() {
		log.Debug("emailoctopus: not configured, skipping Unsubscribe")
		return nil
	}

	url := fmt.Sprintf("%s/lists/%s/contacts/%s", baseURL, c.cfg.ListID, contactID(email))
	resp, err := c.send(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		// Contact didn't exist. End-state matches caller intent —
		// they're not subscribed. Return nil so the caller stamps
		// marketing_synced_at and stops retrying.
		return nil
	default:
		return fmt.Errorf("unsubscribe: status %d: %s", resp.StatusCode, readBody(resp))
	}
}

// UpdateContact refreshes a contact's tag + (optionally) status. If
// `ensureSubscribed` is true, also bumps their status to SUBSCRIBED —
// used by Subscribe's 409-fallback to make the overall Subscribe
// operation idempotent.
//
// `name` is accepted but not forwarded to EmailOctopus — see
// Subscribe's docstring for the reasoning around custom fields.
func (c *Connector) UpdateContact(email, name string, ensureSubscribed bool) error {
	if !c.Configured() {
		log.Debug("emailoctopus: not configured, skipping UpdateContact")
		return nil
	}
	_ = name // intentionally unused — see Subscribe docstring

	payload := updatePayload{
		// Always set the platform tag on update too, so a returning
		// user whose contact was created outside the platform gets
		// tagged once they opt in via Flomation. Safe to set
		// repeatedly — EmailOctopus is idempotent on add-existing-tag.
		Tags: map[string]bool{platformTag: true},
	}
	if ensureSubscribed {
		payload.Status = statusSubscribed
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal update payload: %w", err)
	}

	url := fmt.Sprintf("%s/lists/%s/contacts/%s", baseURL, c.cfg.ListID, contactID(email))
	resp, err := c.send(http.MethodPut, url, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	return fmt.Errorf("update: status %d: %s", resp.StatusCode, readBody(resp))
}

// send is a thin shared HTTP helper. All EmailOctopus v2 methods
// authenticate via the Authorization: Bearer header and use JSON
// bodies (where a body is sent at all), so the shared shape keeps
// the public functions readable.
func (c *Connector) send(method, url string, body []byte) (*http.Response, error) {
	var reqBody *bytes.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api call: %w", err)
	}
	return resp, nil
}

// readBody trims the EO response body to a sensible audit length for
// the error message. We're never streaming large responses from EO,
// so a 1KB cap is plenty.
func readBody(resp *http.Response) string {
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
