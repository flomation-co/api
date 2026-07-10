package http

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	gohttp "net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Live dropdowns for the Infrastructure ▸ Kubernetes and ▸ Helm actions.
//
// Every picker (namespaces, pods, deployments, secrets, helm releases…) resolves
// through one of these proxies. There is no fixed upstream — the API server URL
// is per-node configuration — so the editor forwards the node's api_server_url /
// service_account_token / cluster_ca_cert / allow_insecure inputs as query
// parameters, declared via each marker's Params in dynamicOptionsMetadata. The
// token arrives as a ${secrets.X} reference and is resolved server-side, so the
// plaintext never transits the browser.
//
// Two things are worth knowing about the implementation:
//
//   - Every list is fetched with the PartialObjectMetadataList Accept header, so
//     the API server returns object metadata and nothing else. That is what makes
//     a *secrets* dropdown safe: the api learns the secrets' names and never
//     receives their values. It also keeps a 500-object page small.
//   - Unlike the Jenkins/WordPress proxies, the http.Client cannot be a single
//     package-level value: the CA bundle and the insecure opt-in vary per cluster.
//     Clients are therefore built per distinct TLS configuration and memoised.
//
// Errors follow the option-proxy convention of HTTP 200 + {"error": …} so the
// editor renders the message inline and falls back to manual entry.

// ---------------------------------------------------------------------------
// Dropdown registration
// ---------------------------------------------------------------------------

// k8sConnParams are the connection inputs the editor forwards on every
// Kubernetes/Helm option fetch. `environment` is NOT listed: the editor appends
// it to every dynamic-options request already, and it is what lets the
// ${secrets.X} token be resolved server-side.
var k8sConnParams = []string{"api_server_url", "auth_method", "service_account_token", "cluster_ca_cert", "allow_insecure"}

// k8sNamespacedParams add the node's chosen namespace, so a picker lists only
// the objects the operator can actually address.
var k8sNamespacedParams = append(append([]string{}, k8sConnParams...), "namespace")

// nameOptionEndpoint maps an action's resource family to the picker that fills
// its `name` input.
var nameOptionEndpoint = map[string][]string{
	"kubernetes-namespaces":      {"namespace_get", "namespace_delete"},
	"kubernetes-nodes":           {"node_get", "node_cordon", "node_uncordon", "node_drain"},
	"kubernetes-pods":            {"pod_get", "pod_delete", "pod_logs"},
	"kubernetes-deployments":     {"deployment_get", "deployment_scale", "deployment_restart", "deployment_rollout_status", "deployment_delete"},
	"kubernetes-statefulsets":    {"statefulset_get", "statefulset_scale", "statefulset_restart", "statefulset_delete"},
	"kubernetes-daemonsets":      {"daemonset_get", "daemonset_restart", "daemonset_delete"},
	"kubernetes-services":        {"service_get", "service_delete"},
	"kubernetes-ingresses":       {"ingress_get", "ingress_delete"},
	"kubernetes-configmaps":      {"configmap_get", "configmap_update", "configmap_delete"},
	"kubernetes-secrets":         {"secret_get", "secret_delete"},
	"kubernetes-jobs":            {"job_get", "job_delete"},
	"kubernetes-cronjobs":        {"cronjob_get", "cronjob_trigger", "cronjob_suspend", "cronjob_resume", "cronjob_delete"},
	"kubernetes-pvcs":            {"pvc_get", "pvc_delete"},
	"kubernetes-serviceaccounts": {"serviceaccount_get", "serviceaccount_delete"},
	"kubernetes-hpas":            {"hpa_get", "hpa_delete"},
}

// clusterScopedActions have no `namespace` input at all — Namespace and Node are
// cluster-scoped kinds — so their `name` picker must not forward one.
var clusterScopedActions = map[string]bool{
	"namespace_get": true, "namespace_delete": true, "namespace_create": true,
	"node_get": true, "node_cordon": true, "node_uncordon": true, "node_drain": true,
	"node_list": true, "namespace_list": true,
}

// namespacedKubernetesActions get a `namespace` picker. Everything else in the
// Kubernetes sub-group is cluster-scoped.
var namespacedKubernetesActions = []string{
	"pod_list", "pod_get", "pod_delete", "pod_logs",
	"deployment_list", "deployment_get", "deployment_scale", "deployment_restart", "deployment_rollout_status", "deployment_delete",
	"statefulset_list", "statefulset_get", "statefulset_scale", "statefulset_restart", "statefulset_delete",
	"daemonset_list", "daemonset_get", "daemonset_restart", "daemonset_delete",
	"service_list", "service_get", "service_create", "service_delete",
	"ingress_list", "ingress_get", "ingress_delete",
	"configmap_list", "configmap_get", "configmap_create", "configmap_update", "configmap_delete",
	"secret_list", "secret_get", "secret_create", "secret_delete",
	"job_list", "job_get", "job_create", "job_delete",
	"cronjob_list", "cronjob_get", "cronjob_create", "cronjob_trigger", "cronjob_suspend", "cronjob_resume", "cronjob_delete",
	"pvc_list", "pvc_get", "pvc_delete",
	"serviceaccount_list", "serviceaccount_get", "serviceaccount_create", "serviceaccount_delete",
	"hpa_list", "hpa_get", "hpa_delete",
	"event_list", "apply_manifest",
}

// helmNamespacedActions get a `namespace` picker; helmReleaseActions also get a
// `name` picker backed by the cluster's Helm storage Secrets.
var helmNamespacedActions = []string{
	"release_list", "release_get", "release_status", "release_history",
	"release_install", "release_upgrade", "release_rollback", "release_uninstall", "release_test",
	"chart_template",
}

var helmReleaseActions = []string{
	"release_get", "release_status", "release_history",
	"release_upgrade", "release_rollback", "release_uninstall", "release_test",
}

// init registers the Kubernetes and Helm live dropdowns into the shared
// dynamicOptionsMetadata map.
//
// They are registered from a table rather than spelled out as ~120 literal map
// entries, because the pattern is entirely regular — every namespaced action gets
// a namespace picker, and every action addressing an existing object gets a name
// picker for its own kind. Package-level variables are initialised before any
// init() runs, so dynamicOptionsMetadata is non-nil here.
func init() {
	register := func(actionID, input, endpoint string, params []string) {
		dynamicOptionsMetadata[actionID+"#"+input] = api.InputDynamicOptions{
			Endpoint: "/api/v1/action/options/" + endpoint,
			Params:   params,
		}
	}

	for _, verb := range namespacedKubernetesActions {
		register("infrastructure/kubernetes/"+verb, "namespace", "kubernetes-namespaces", k8sConnParams)
	}
	for endpoint, verbs := range nameOptionEndpoint {
		for _, verb := range verbs {
			params := k8sNamespacedParams
			if clusterScopedActions[verb] {
				params = k8sConnParams
			}
			register("infrastructure/kubernetes/"+verb, "name", endpoint, params)
		}
	}

	// pod_logs' container picker needs the pod as well as the namespace.
	register("infrastructure/kubernetes/pod_logs", "container", "kubernetes-containers",
		append(append([]string{}, k8sNamespacedParams...), "name"))

	for _, verb := range helmNamespacedActions {
		register("infrastructure/helm/"+verb, "namespace", "kubernetes-namespaces", k8sConnParams)
	}
	for _, verb := range helmReleaseActions {
		register("infrastructure/helm/"+verb, "name", "helm-releases", k8sNamespacedParams)
	}
}

// metadataOnlyAccept asks the API server for PartialObjectMetadata rather than
// whole objects. Served since Kubernetes 1.15; a cluster that does not understand
// it simply returns the full object, and the name extraction below still works.
const metadataOnlyAccept = "application/json;as=PartialObjectMetadataList;v=v1;g=meta.k8s.io, application/json"

// k8sOptionListLimit bounds a dropdown page. A namespace with more objects than
// this is not usefully browsed from a select box; the operator types the name.
const k8sOptionListLimit = 500

// k8sOptionResource locates a kind on the API server. It mirrors the executor's
// kubernetes.Resource, kept separate because the api must not depend on the
// executor module.
type k8sOptionResource struct {
	APIRoot    string
	Plural     string
	Namespaced bool
}

// k8sOptionResources is the set of kinds that back a live dropdown, keyed by the
// slug in the route (/action/options/kubernetes-<slug>).
var k8sOptionResources = map[string]k8sOptionResource{
	"namespaces":      {APIRoot: "/api/v1", Plural: "namespaces", Namespaced: false},
	"nodes":           {APIRoot: "/api/v1", Plural: "nodes", Namespaced: false},
	"pods":            {APIRoot: "/api/v1", Plural: "pods", Namespaced: true},
	"services":        {APIRoot: "/api/v1", Plural: "services", Namespaced: true},
	"configmaps":      {APIRoot: "/api/v1", Plural: "configmaps", Namespaced: true},
	"secrets":         {APIRoot: "/api/v1", Plural: "secrets", Namespaced: true},
	"pvcs":            {APIRoot: "/api/v1", Plural: "persistentvolumeclaims", Namespaced: true},
	"serviceaccounts": {APIRoot: "/api/v1", Plural: "serviceaccounts", Namespaced: true},
	"deployments":     {APIRoot: "/apis/apps/v1", Plural: "deployments", Namespaced: true},
	"statefulsets":    {APIRoot: "/apis/apps/v1", Plural: "statefulsets", Namespaced: true},
	"daemonsets":      {APIRoot: "/apis/apps/v1", Plural: "daemonsets", Namespaced: true},
	"jobs":            {APIRoot: "/apis/batch/v1", Plural: "jobs", Namespaced: true},
	"cronjobs":        {APIRoot: "/apis/batch/v1", Plural: "cronjobs", Namespaced: true},
	"ingresses":       {APIRoot: "/apis/networking.k8s.io/v1", Plural: "ingresses", Namespaced: true},
	"hpas":            {APIRoot: "/apis/autoscaling/v2", Plural: "horizontalpodautoscalers", Namespaced: true},
}

// ---------------------------------------------------------------------------
// TLS-aware, SSRF-hardened client
// ---------------------------------------------------------------------------

var (
	k8sClientCacheMu sync.Mutex
	k8sClientCache   = map[string]*gohttp.Client{}
)

// k8sOptionsDialControl refuses link-local and cloud-metadata destinations. It
// runs on the address actually dialled, so a DNS name or a redirect resolving to
// one of them is caught too. Loopback and private LAN ranges stay allowed — a
// self-hosted Kubernetes API server almost always lives there, exactly as with
// the Jenkins and WordPress proxies.
func k8sOptionsDialControl(_, address string, _ syscall.RawConn) error {
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

// kubernetesOptionsClient builds (or reuses) a client for one cluster's TLS
// material. The secure and insecure paths are separate configurations, never a
// mutation of a shared one, so the default can't be weakened by a stray request.
func kubernetesOptionsClient(caPEM string, insecure bool) (*gohttp.Client, error) {
	key := fmt.Sprintf("%t\x00%s", insecure, caPEM)

	k8sClientCacheMu.Lock()
	defer k8sClientCacheMu.Unlock()
	if c, ok := k8sClientCache[key]; ok {
		return c, nil
	}

	// #nosec G402 -- InsecureSkipVerify is an explicit per-node opt-in
	// (allow_insecure) for self-signed clusters, never the default.
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if insecure {
		tlsCfg.InsecureSkipVerify = true
	} else if caPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("cluster CA certificate is not valid PEM")
		}
		tlsCfg.RootCAs = pool
	}

	c := &gohttp.Client{
		Timeout: 10 * time.Second,
		// The dial Control blocks metadata IPs even mid-redirect, but a redirect to
		// a different host in an allowed (private) range would still be followed —
		// so cross-host redirects are refused outright. The Kubernetes API server
		// answers directly and never needs to leave the host.
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
			TLSClientConfig: tlsCfg,
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
				Control: k8sOptionsDialControl,
			}).DialContext,
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     60 * time.Second,
		},
	}
	k8sClientCache[key] = c
	return c, nil
}

// ---------------------------------------------------------------------------
// Connection resolution
// ---------------------------------------------------------------------------

type k8sProxyConn struct {
	Server   string
	Token    string
	CACert   string
	Insecure bool
}

// resolveKubernetesConn reads the node's connection out of the query parameters,
// resolving the token secret server-side. The returned message, when non-empty,
// is the operator-facing text to render in place of the dropdown; the caller must
// stop. An empty message with ok==false means the response was already written.
func (s *Service) resolveKubernetesConn(c *gin.Context) (k8sProxyConn, string, bool) {
	server, err := kubernetesServerURL(strings.TrimSpace(c.Query("api_server_url")))
	if err != nil {
		// Also the path taken when api_server_url holds an unresolved ${...}.
		return k8sProxyConn{}, "Set the API Server URL (a full https:// URL) to load this list", false
	}

	// Only the Service Account Token method can back a dropdown. A client
	// certificate or a pasted kubeconfig would have to be resolved and parsed
	// here, duplicating the executor's auth handling in the api for a
	// convenience feature; the flow itself still runs on those methods.
	if method := strings.TrimSpace(c.Query("auth_method")); method != "" && method != "token" {
		return k8sProxyConn{}, "Live lists need the Service Account Token authentication method — type the name manually (the flow still runs)", false
	}

	token := strings.TrimSpace(c.Query("service_account_token"))
	if strings.HasPrefix(token, "${credentials.") || strings.HasPrefix(token, "${credential.") {
		return k8sProxyConn{}, "Managed credentials can't be used to load this list — use an environment secret for the token (the flow itself still runs)", false
	}
	if strings.HasPrefix(token, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			return k8sProxyConn{}, "Select an environment to resolve the service account token", false
		}
		// Resolving a secret to plaintext must be gated by the same permission as
		// reading it through the environment endpoints: the resolved value
		// authenticates a request to a caller-supplied host, so without this check
		// a member denied environment.view could exfiltrate any secret by aiming
		// the proxy at a server they control.
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return k8sProxyConn{}, "", false // checkPermission has written the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, token)
		if errMsg != "" {
			return k8sProxyConn{}, errMsg, false
		}
		token = resolved
	}
	if token == "" {
		return k8sProxyConn{}, "Set the Service Account Token to load this list", false
	}

	caCert := strings.TrimSpace(c.Query("cluster_ca_cert"))
	if strings.HasPrefix(caCert, "${") {
		// A variable reference the editor could not resolve; treat as absent
		// rather than sending "${var.x}" to x509.
		caCert = ""
	}

	insecure := strings.EqualFold(strings.TrimSpace(c.Query("allow_insecure")), "true")
	if caCert != "" && !strings.Contains(caCert, "BEGIN CERTIFICATE") {
		return k8sProxyConn{}, "The Cluster CA Certificate is not a PEM certificate — clear it, or tick Allow Insecure TLS", false
	}

	return k8sProxyConn{Server: server, Token: token, CACert: caCert, Insecure: insecure}, "", true
}

// kubernetesServerURL normalises a user-supplied API server URL to
// scheme://host[:port], dropping any path, query, and embedded credentials so a
// crafted base cannot displace the API path we append.
func kubernetesServerURL(base string) (string, error) {
	if base == "" {
		return "", errors.New("api_server_url is required")
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errors.New("api_server_url must be a full http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("api_server_url must be http or https")
	}
	return u.Scheme + "://" + u.Host, nil
}

// ---------------------------------------------------------------------------
// Fetching
// ---------------------------------------------------------------------------

type k8sStatusError struct{ status int }

func (e *k8sStatusError) Error() string {
	return fmt.Sprintf("Kubernetes API returned status %d %s", e.status, gohttp.StatusText(e.status))
}

// partialMetadataList is the shape of both a PartialObjectMetadataList and an
// ordinary List, as far as this file cares: a slice of objects each carrying a
// metadata.name and metadata.labels.
type partialMetadataList struct {
	Items []struct {
		Metadata struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
	} `json:"items"`
}

func fetchKubernetesList(c *gin.Context, conn k8sProxyConn, path string, query url.Values) (*partialMetadataList, error) {
	client, err := kubernetesOptionsClient(conn.CACert, conn.Insecure)
	if err != nil {
		return nil, err
	}

	if query == nil {
		query = url.Values{}
	}
	query.Set("limit", fmt.Sprint(k8sOptionListLimit))

	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, conn.Server+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	req.Header.Set("Accept", metadataOnlyAccept)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != gohttp.StatusOK {
		return nil, &k8sStatusError{status: resp.StatusCode}
	}

	var out partialMetadataList
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// kubernetesOptionsError maps a fetch failure to operator-facing text.
func kubernetesOptionsError(err error, what string) string {
	var statusErr *k8sStatusError
	switch {
	case errors.As(err, &statusErr) && statusErr.status == gohttp.StatusUnauthorized:
		return "The cluster rejected the token — check the Service Account Token"
	case errors.As(err, &statusErr) && statusErr.status == gohttp.StatusForbidden:
		return fmt.Sprintf("The service account is not allowed to list %s — grant it read access, or type the name manually", what)
	case errors.As(err, &statusErr) && statusErr.status == gohttp.StatusNotFound:
		return fmt.Sprintf("The cluster does not serve %s at the expected API version", what)
	case errors.As(err, &statusErr):
		return fmt.Sprintf("The cluster returned an unexpected response (HTTP %d)", statusErr.status)
	default:
		return "Could not reach the cluster — check the API Server URL, the CA certificate, and that the API server is running"
	}
}

func namesToOptions(list *partialMetadataList) []api.InputOption {
	options := make([]api.InputOption, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Metadata.Name == "" {
			continue
		}
		options = append(options, api.InputOption{Name: item.Metadata.Name, Value: item.Metadata.Name})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	return options
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// kubernetesOptions serves the names of one kind as dropdown options. A
// namespaced kind reads the node's `namespace` input; when it is blank the list
// spans every namespace, which is what the *_list actions do too.
func (s *Service) kubernetesOptions(slug string) gin.HandlerFunc {
	resource, known := k8sOptionResources[slug]
	if !known {
		panic("kubernetesOptions: unknown resource slug " + slug)
	}

	return func(c *gin.Context) {
		conn, errMsg, ok := s.resolveKubernetesConn(c)
		if !ok {
			if errMsg != "" {
				c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			}
			return
		}

		path := resource.APIRoot
		if resource.Namespaced {
			if ns := strings.TrimSpace(c.Query("namespace")); ns != "" && !strings.HasPrefix(ns, "${") {
				path += "/namespaces/" + url.PathEscape(ns)
			}
		}
		path += "/" + resource.Plural

		list, err := fetchKubernetesList(c, conn, path, nil)
		if err != nil {
			log.WithFields(log.Fields{"error": err, "resource": slug}).Warn("unable to fetch Kubernetes options")
			c.JSON(gohttp.StatusOK, gin.H{"error": kubernetesOptionsError(err, slug)})
			return
		}
		c.JSON(gohttp.StatusOK, gin.H{"options": namesToOptions(list)})
	}
}

// getKubernetesContainers lists the containers of one pod, for the Container
// input of pod_logs. It reads a single Pod rather than a collection, and needs
// the full object — a container list lives in .spec, which PartialObjectMetadata
// deliberately omits.
func (s *Service) getKubernetesContainers(c *gin.Context) {
	conn, errMsg, ok := s.resolveKubernetesConn(c)
	if !ok {
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		}
		return
	}

	namespace := strings.TrimSpace(c.Query("namespace"))
	pod := strings.TrimSpace(c.Query("name"))
	if namespace == "" || strings.HasPrefix(namespace, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Namespace to load the container list"})
		return
	}
	if pod == "" || strings.HasPrefix(pod, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select a Pod to load its container list"})
		return
	}

	client, err := kubernetesOptionsClient(conn.CACert, conn.Insecure)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "The Cluster CA Certificate is not valid PEM"})
		return
	}

	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(pod)
	req, err := gohttp.NewRequestWithContext(c.Request.Context(), gohttp.MethodGet, conn.Server+path, nil)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not build the request for the container list"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.WithField("error", err).Warn("unable to fetch Kubernetes pod containers")
		c.JSON(gohttp.StatusOK, gin.H{"error": kubernetesOptionsError(err, "pods")})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || resp.StatusCode != gohttp.StatusOK {
		c.JSON(gohttp.StatusOK, gin.H{"error": kubernetesOptionsError(&k8sStatusError{status: resp.StatusCode}, "pods")})
		return
	}

	var podObj struct {
		Spec struct {
			Containers []struct {
				Name string `json:"name"`
			} `json:"containers"`
			InitContainers []struct {
				Name string `json:"name"`
			} `json:"initContainers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &podObj); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Could not read the pod's container list"})
		return
	}

	options := make([]api.InputOption, 0, len(podObj.Spec.Containers)+len(podObj.Spec.InitContainers))
	for _, ct := range podObj.Spec.Containers {
		options = append(options, api.InputOption{Name: ct.Name, Value: ct.Name})
	}
	// Init containers can be logged too, and are often exactly what an operator
	// wants when a pod is stuck in Init:CrashLoopBackOff.
	for _, ct := range podObj.Spec.InitContainers {
		options = append(options, api.InputOption{Name: ct.Name + " (init)", Value: ct.Name})
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}

// getHelmReleases lists the Helm releases in a namespace, for the Release input
// of the helm actions.
//
// Helm stores each revision as a Secret labelled owner=helm,name=<release>. The
// release names are read from those labels — no payload is decoded, and the
// metadata-only Accept header means the api never receives the Secrets' data.
func (s *Service) getHelmReleases(c *gin.Context) {
	conn, errMsg, ok := s.resolveKubernetesConn(c)
	if !ok {
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
		}
		return
	}

	path := "/api/v1"
	if ns := strings.TrimSpace(c.Query("namespace")); ns != "" && !strings.HasPrefix(ns, "${") {
		path += "/namespaces/" + url.PathEscape(ns)
	}
	path += "/secrets"

	list, err := fetchKubernetesList(c, conn, path, url.Values{"labelSelector": {"owner=helm"}})
	if err != nil {
		log.WithField("error", err).Warn("unable to fetch Helm releases")
		c.JSON(gohttp.StatusOK, gin.H{"error": kubernetesOptionsError(err, "Helm releases")})
		return
	}

	seen := map[string]struct{}{}
	options := make([]api.InputOption, 0, len(list.Items))
	for _, item := range list.Items {
		name := item.Metadata.Labels["name"]
		if name == "" {
			continue
		}
		// A release has one Secret per revision; collapse them to one option.
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		options = append(options, api.InputOption{Name: name, Value: name})
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	c.JSON(gohttp.StatusOK, gin.H{"options": options})
}
