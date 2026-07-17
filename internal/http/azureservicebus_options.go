package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	gohttp "net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"flomation.app/automate/api"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Azure Service Bus — live dropdowns for queues, topics and subscriptions
// ---------------------------------------------------------------------------
//
// The executor node speaks AMQP 1.0 through the SDK, because the REST surface
// cannot do sessions, batching or deferral. But AMQP cannot ENUMERATE entities
// — there is no "list the queues" frame. Enumeration lives in a completely
// separate protocol: the ATOM/XML management API over HTTPS, which is what
// these proxies speak. So this file deliberately does not mirror the node's
// transport, because the thing it needs does not exist there.
//
// Two consequences worth knowing:
//
//   - The LOCAL EMULATOR CANNOT SERVE THESE LISTS. It publishes AMQP 5672 and
//     nothing else; its entities come from a static JSON config file, and it
//     has no management endpoint at all. Against an emulator connection string
//     these proxies fail closed with a message saying so, and the operator
//     types the name — which is fine, because they wrote that JSON file
//     themselves and already know the names.
//
//   - The ATOM API predates the modern Azure REST conventions: it is XML, it
//     is versioned by a query parameter, and a missing entity is signalled by
//     an EMPTY FEED rather than a 404.

// azureServiceBusConnParams are the connection inputs the editor forwards for
// the queue/topic lists.
// `environment` is absent by design — the editor injects it after walking
// Params, since it is not an input on the node.
var azureServiceBusConnParams = []string{
	"auth_method", "connection_string", "namespace",
	"azure_tenant_id", "azure_client_id", "azure_client_secret",
}

// azureServiceBusSubscriptionParams additionally carry the topic, since
// subscriptions are enumerated beneath one.
var azureServiceBusSubscriptionParams = []string{
	"auth_method", "connection_string", "namespace", "topic",
	"azure_tenant_id", "azure_client_id", "azure_client_secret",
}

// azureServiceBusQueueActions are the actions with a `queue` input naming an
// existing queue (queue_get_all lists them; queue_create names a new one).
var azureServiceBusQueueActions = []string{
	"deadletter_peek", "deadletter_receive", "message_dead_letter",
	"queue_cancel_scheduled", "queue_delete", "queue_get", "queue_peek",
	"queue_receive", "queue_receive_deferred", "queue_runtime_properties",
	"queue_schedule", "queue_send", "queue_send_batch", "queue_update",
	"session_receive",
}

// azureServiceBusTopicActions are the actions with a `topic` input naming an
// existing topic. Several are dual-entity actions (they operate on a queue OR a
// topic+subscription, switched by entity_type), which is why they appear in
// both this list and the queue one.
var azureServiceBusTopicActions = []string{
	"deadletter_peek", "deadletter_receive", "message_dead_letter",
	"queue_receive_deferred", "rule_create", "rule_delete", "rule_list",
	"session_receive", "subscription_create", "subscription_delete",
	"subscription_list", "subscription_peek", "subscription_receive",
	"subscription_runtime_properties", "topic_delete", "topic_send",
}

// azureServiceBusSubscriptionActions are the actions with a `subscription`
// input naming an existing subscription (subscription_create names a new one).
var azureServiceBusSubscriptionActions = []string{
	"deadletter_peek", "deadletter_receive", "message_dead_letter",
	"queue_receive_deferred", "rule_create", "rule_delete", "rule_list",
	"session_receive", "subscription_delete", "subscription_peek",
	"subscription_receive", "subscription_runtime_properties",
}

func init() {
	register := func(actionID, input, endpoint string, params []string) {
		dynamicOptionsMetadata[actionID+"#"+input] = api.InputDynamicOptions{
			Endpoint: "/api/v1/action/options/" + endpoint,
			Params:   params,
		}
	}
	for _, a := range azureServiceBusQueueActions {
		register("messagebrokers/azureservicebus/"+a, "queue", "azure-servicebus-queues", azureServiceBusConnParams)
	}
	for _, a := range azureServiceBusTopicActions {
		register("messagebrokers/azureservicebus/"+a, "topic", "azure-servicebus-topics", azureServiceBusConnParams)
	}
	for _, a := range azureServiceBusSubscriptionActions {
		register("messagebrokers/azureservicebus/"+a, "subscription", "azure-servicebus-subscriptions", azureServiceBusSubscriptionParams)
	}
}

// azureServiceBusAPIVersion is the ATOM management API version. It is passed as
// a query parameter, not a header — this API predates the x-ms-version
// convention the rest of Azure uses.
const azureServiceBusAPIVersion = "2021-05"

// azureServiceBusSASTTL is how long a minted SAS is valid. These tokens are
// used for exactly one list call and then discarded, so the window only needs
// to cover clock skew plus the request.
const azureServiceBusSASTTL = 5 * time.Minute

// azureServiceBusConnString is a parsed Service Bus connection string.
type azureServiceBusConnString struct {
	Namespace  string // fully-qualified host, e.g. ns.servicebus.windows.net
	KeyName    string
	Key        string
	IsEmulator bool
}

// parseAzureServiceBusConnString reads the semicolon-delimited connection
// string Azure hands out.
//
// The emulator's string is the reason this returns IsEmulator rather than just
// failing: it carries UseDevelopmentEmulator=true and an Endpoint of
// sb://localhost, which is syntactically fine but has no management API behind
// it. Detecting it here lets the caller explain that, instead of emitting a
// connection error the operator would waste time debugging.
// The second return is an OPERATOR-FACING message, not a Go error, which is
// why it is a string: these sentences are shown verbatim in the editor, so they
// are capitalised and complete. Returning them as `error` would mean either
// lowercase fragments in the UI or a lint suppression, and neither is worth it
// for a value no caller ever wraps or compares.
func parseAzureServiceBusConnString(raw string) (azureServiceBusConnString, string) {
	var out azureServiceBusConnString
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "${") {
		return out, "Set the Connection String to load this list"
	}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// SplitN(2): the shared key is base64 and routinely contains '='.
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, value := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch strings.ToLower(key) {
		case "endpoint":
			u, err := url.Parse(value)
			if err != nil || u.Host == "" {
				return out, "The Connection String's Endpoint is not a valid URL"
			}
			out.Namespace = strings.ToLower(u.Hostname())
		case "sharedaccesskeyname":
			out.KeyName = value
		case "sharedaccesskey":
			out.Key = value
		case "usedevelopmentemulator":
			out.IsEmulator = strings.EqualFold(value, "true")
		}
	}
	if out.Namespace == "" {
		return out, "The Connection String is missing its Endpoint"
	}
	if out.KeyName == "" || out.Key == "" {
		return out, "The Connection String is missing its SharedAccessKeyName or SharedAccessKey"
	}
	return out, ""
}

// azureServiceBusSASToken mints a Shared Access Signature over a resource URI.
//
// The signed payload is url-encoded(uri) + "\n" + expiry-as-unix-seconds, and
// the SIGNATURE ITSELF is then url-encoded when placed in the header — encoding
// it once or twice both produce a plausible-looking token, and only the correct
// one authenticates.
//
// NOTE the key handling, which is the opposite of its neighbour: a Service Bus
// SharedAccessKey is signed as RAW UTF-8 BYTES, whereas the Storage account key
// in azure_options.go must be base64-DECODED first. Both keys look like base64,
// so decoding this one produces a perfectly well-formed token that simply never
// authenticates. Do not "fix" this to match the storage signer.
func azureServiceBusSASToken(resourceURI, keyName, key string) string {
	encodedURI := url.QueryEscape(strings.ToLower(resourceURI))
	expiry := strconv.FormatInt(time.Now().Add(azureServiceBusSASTTL).Unix(), 10)

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(encodedURI + "\n" + expiry))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return "SharedAccessSignature sr=" + encodedURI +
		"&sig=" + url.QueryEscape(signature) +
		"&se=" + expiry +
		"&skn=" + url.QueryEscape(keyName)
}

// azureServiceBusEntityPattern pins entity names before they are placed in a
// URL path. Service Bus allows letters, digits, and . - _ ~, plus / for
// hierarchical names — but / is excluded here because these proxies only build
// single-segment paths and allowing it would let a value climb the path.
var azureServiceBusEntityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-~]{0,259}$`)

// azureHostPattern accepts a fully-qualified host so sovereign-cloud
// namespaces (…servicebus.chinacloudapi.cn and friends) are usable, while
// still refusing anything that could carry a port, path, or credentials into
// the URL being built.
var azureHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)

// azureServiceBusFeed is the ATOM envelope every list call returns. The title
// of each entry is the entity's name.
type azureServiceBusFeed struct {
	Entries []struct {
		Title string `xml:"title"`
	} `xml:"entry"`
}

// azureServiceBusResolve works out the namespace host and the Authorization
// header for one list call, honouring both auth methods the node offers.
func (s *Service) azureServiceBusResolve(c *gin.Context) (host, authHeader string, ok bool) {
	method := strings.ToLower(strings.TrimSpace(c.Query("auth_method")))
	if method == "" {
		method = "connection_string"
	}

	switch method {
	case "connection_string":
		raw, resolved := s.resolveAzureSecretParam(c, "connection_string", "Connection String")
		if !resolved {
			return "", "", false
		}
		conn, errMsg := parseAzureServiceBusConnString(raw)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return "", "", false
		}
		if conn.IsEmulator {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The local emulator can't list entities — it only speaks AMQP, and its queues and topics come from its config file. Type the name instead (the flow itself still runs)."})
			return "", "", false
		}
		resourceURI := "https://" + conn.Namespace + "/"
		return conn.Namespace, azureServiceBusSASToken(resourceURI, conn.KeyName, conn.Key), true

	case "entra":
		namespace := strings.TrimSpace(c.Query("namespace"))
		if namespace == "" || strings.HasPrefix(namespace, "${") {
			c.JSON(gohttp.StatusOK, gin.H{"error": "Set the Namespace to load this list"})
			return "", "", false
		}
		// A bare namespace label is qualified to the public-cloud host; a
		// fully-qualified name (sovereign clouds) is taken as given.
		if !strings.Contains(namespace, ".") {
			if !azureNamePattern.MatchString(namespace) {
				c.JSON(gohttp.StatusOK, gin.H{"error": "The Namespace may contain only letters, digits and dashes"})
				return "", "", false
			}
			namespace += ".servicebus.windows.net"
		} else if !azureHostPattern.MatchString(namespace) {
			c.JSON(gohttp.StatusOK, gin.H{"error": "The Namespace is not a valid host name"})
			return "", "", false
		}
		tenantID, clientID, clientSecret, resolved := s.resolveAzureServicePrincipal(c)
		if !resolved {
			return "", "", false
		}
		token, errMsg := azureClientCredentialsToken(c, tenantID, clientID, clientSecret, "https://servicebus.azure.net/.default")
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return "", "", false
		}
		return strings.ToLower(namespace), "Bearer " + token, true

	default:
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Authentication method must be Connection String or Microsoft Entra"})
		return "", "", false
	}
}

// azureServiceBusList issues one ATOM management GET and returns the entity
// names from the feed.
func azureServiceBusList(c *gin.Context, host, authHeader, path string) ([]string, bool) {
	endpoint := "https://" + host + path + "?api-version=" + azureServiceBusAPIVersion + "&$top=1000"
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, endpoint, nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the Azure Service Bus request"})
		return nil, false
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/atom+xml")

	body, errMsg := doAzureOptionsGet(azureOptionsHTTPClient, req, "Azure Service Bus")
	if errMsg != "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		return nil, false
	}

	var feed azureServiceBusFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Failed to parse the Azure Service Bus response"})
		return nil, false
	}
	names := make([]string, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		if name := strings.TrimSpace(e.Title); name != "" {
			names = append(names, name)
		}
	}
	return names, true
}

func azureServiceBusWrite(c *gin.Context, names []string) {
	options := make([]api.InputOption, 0, len(names))
	for _, n := range names {
		options = append(options, api.InputOption{Name: n, Value: n})
	}
	writeAzureOptions(c, options)
}

func (s *Service) getAzureServiceBusQueues(c *gin.Context) {
	host, auth, ok := s.azureServiceBusResolve(c)
	if !ok {
		return
	}
	names, ok := azureServiceBusList(c, host, auth, "/$Resources/Queues")
	if !ok {
		return
	}
	azureServiceBusWrite(c, names)
}

func (s *Service) getAzureServiceBusTopics(c *gin.Context) {
	host, auth, ok := s.azureServiceBusResolve(c)
	if !ok {
		return
	}
	names, ok := azureServiceBusList(c, host, auth, "/$Resources/Topics")
	if !ok {
		return
	}
	azureServiceBusWrite(c, names)
}

func (s *Service) getAzureServiceBusSubscriptions(c *gin.Context) {
	topic := strings.TrimSpace(c.Query("topic"))
	if topic == "" || strings.HasPrefix(topic, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Topic to load this list"})
		return
	}
	if !azureServiceBusEntityPattern.MatchString(topic) {
		c.JSON(gohttp.StatusOK, gin.H{"error": "That Topic name is not valid"})
		return
	}
	host, auth, ok := s.azureServiceBusResolve(c)
	if !ok {
		return
	}
	names, ok := azureServiceBusList(c, host, auth, "/"+url.PathEscape(topic)+"/Subscriptions")
	if !ok {
		return
	}
	azureServiceBusWrite(c, names)
}
