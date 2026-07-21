package http

import (
	"errors"
	"fmt"
	gohttp "net/http"
	"net"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
)

// Live dropdown option proxies for the Oracle Cloud Compute node. Unlike the
// bearer/basic-auth proxies, OCI uses API request signing, so these build an OCI
// ConfigurationProvider from the node's connection (the private key is resolved
// server-side from ${secrets.X}) and call the identity/core list APIs.
//
// Registration of the dynamicOptionsMetadata markers happens in init() below
// (the same pattern as azure/kubernetes/awx), keyed "<actionID>#<inputName>".

// validOCIRegion mirrors the executor's guard: a region selects the API host, so
// it must be a plain label — a dotted value would let the SDK build an
// arbitrary host. With this, the host is always *.oraclecloud.com.
var validOCIRegion = regexp.MustCompile(`^[a-z0-9-]+$`)

// ociOptionsMaxPages caps how many pages any option proxy will pull, so a large
// catalogue (Oracle's platform images alone are 200+) can't turn one dropdown
// fetch into an unbounded walk — the same guard the other proxies apply.
const ociOptionsMaxPages = 10

// ociOptionsHTTPClient is SSRF-hardened defence in depth (region validation
// already pins the host to oraclecloud.com): block link-local + cloud-metadata
// IPs at dial time and refuse cross-host redirects.
var ociOptionsHTTPClient = &gohttp.Client{
	Timeout: 12 * time.Second,
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
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
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
		}}).DialContext,
	},
}

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")
	credsCompVCN := append(append([]string{}, credsComp...), "vcn_ocid")
	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/compute/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}
	for _, a := range []string{"instance_get_all", "instance_launch", "instance_list_vnics", "shape_get_all", "image_get_all", "availability_domain_get_all", "compartment_get_all", "vcn_get_all", "subnet_get_all", "boot_volume_get_all"} {
		reg(a, "compartment_ocid", "/api/v1/action/options/oracle-compartments", creds)
	}
	for _, a := range []string{"shape_get_all", "boot_volume_get_all", "instance_launch"} {
		reg(a, "availability_domain", "/api/v1/action/options/oracle-availability-domains", credsComp)
	}
	for _, a := range []string{"image_get_all", "instance_launch"} {
		reg(a, "shape", "/api/v1/action/options/oracle-shapes", credsComp)
	}
	reg("instance_launch", "image_ocid", "/api/v1/action/options/oracle-images", credsComp)
	for _, a := range []string{"subnet_get_all", "instance_launch"} {
		reg(a, "vcn_ocid", "/api/v1/action/options/oracle-vcns", credsComp)
	}
	reg("instance_launch", "subnet_ocid", "/api/v1/action/options/oracle-subnets", credsCompVCN)

	// Object Storage: the compartment picker on the two compartment-scoped bucket
	// actions reuses the same oracle-compartments proxy (creds only, no dependency).
	for _, a := range []string{"bucket_create", "bucket_get_all"} {
		dynamicOptionsMetadata["oracle/objectstorage/"+a+"#compartment_ocid"] = api.InputDynamicOptions{Endpoint: "/api/v1/action/options/oracle-compartments", Params: creds}
	}

	// Autonomous Database: compartment picker on the compartment-scoped actions
	// (reuse oracle-compartments, creds-only). Per-database actions take a plain
	// database OCID with no picker — matching the Compute node's instance_ocid,
	// since a per-database action has no compartment field to scope a DB list.
	for _, a := range []string{"db_get_all", "db_create", "db_clone", "db_list_versions", "db_list_clones", "db_list_backups"} {
		dynamicOptionsMetadata["oracle/autonomousdatabase/"+a+"#compartment_ocid"] = api.InputDynamicOptions{Endpoint: "/api/v1/action/options/oracle-compartments", Params: creds}
	}
	// The destination compartment on the move action is also a real compartment field.
	dynamicOptionsMetadata["oracle/autonomousdatabase/db_change_compartment#target_compartment_ocid"] = api.InputDynamicOptions{Endpoint: "/api/v1/action/options/oracle-compartments", Params: creds}
}

// buildOCIProvider assembles an OCI ConfigurationProvider from the query params
// the editor forwards (the node's connection). The private key + passphrase are
// ${secrets.X} references resolved server-side. On any problem it writes the
// graceful HTTP 200 + {"error": ...} (or a 403 for a permission failure) and
// returns ok=false, so callers just `if !ok { return }`.
func (s *Service) buildOCIProvider(c *gin.Context) (common.ConfigurationProvider, bool) {
	tenancy := strings.TrimSpace(c.Query("tenancy_ocid"))
	user := strings.TrimSpace(c.Query("user_ocid"))
	region := strings.ToLower(strings.TrimSpace(c.Query("region")))
	fingerprint := strings.TrimSpace(c.Query("fingerprint"))
	// Checked in form order (not a map — map iteration is non-deterministic, which
	// would show a random "fill in X first" when several fields are blank), so an
	// operator filling top-to-bottom is always prompted for the first empty field.
	switch {
	case tenancy == "":
		c.JSON(gohttp.StatusOK, gin.H{"error": "Fill in the Tenancy OCID first"})
		return nil, false
	case user == "":
		c.JSON(gohttp.StatusOK, gin.H{"error": "Fill in the User OCID first"})
		return nil, false
	case region == "":
		c.JSON(gohttp.StatusOK, gin.H{"error": "Fill in the Region first"})
		return nil, false
	case fingerprint == "":
		c.JSON(gohttp.StatusOK, gin.H{"error": "Fill in the Key Fingerprint first"})
		return nil, false
	}
	if !validOCIRegion.MatchString(region) {
		c.JSON(gohttp.StatusOK, gin.H{"error": fmt.Sprintf("Region %q is not a valid OCI region", region)})
		return nil, false
	}

	// The private key is always a secret reference → an environment + view
	// permission are required to resolve it.
	keyRef := strings.TrimSpace(c.Query("private_key"))
	if keyRef == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Pick the Private Key secret first"})
		return nil, false
	}
	environmentID := strings.TrimSpace(c.Query("environment"))
	if environmentID == "" {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Select an environment to resolve the Private Key"})
		return nil, false
	}
	if !s.checkPermission(c, rbac.EnvironmentView) {
		return nil, false
	}
	key, ok := s.resolveOCISecret(c, environmentID, keyRef, "Private Key")
	if !ok {
		return nil, false
	}
	var passPtr *string
	if passRef := strings.TrimSpace(c.Query("private_key_passphrase")); passRef != "" {
		pass, ok := s.resolveOCISecret(c, environmentID, passRef, "Passphrase")
		if !ok {
			return nil, false
		}
		if pass != "" {
			passPtr = &pass
		}
	}

	provider := common.NewRawConfigurationProvider(tenancy, user, region, fingerprint, key, passPtr)
	if _, err := provider.PrivateRSAKey(); err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": "The private key could not be parsed — check the secret holds the full PEM"})
		return nil, false
	}
	return provider, true
}

// resolveOCISecret turns a ${secrets.X} reference into its plaintext, writing the
// graceful error and returning ok=false on any problem. A managed-credential
// reference is rejected (OCI is keys-only for now). A plain value passes through.
func (s *Service) resolveOCISecret(c *gin.Context, environmentID, ref, label string) (string, bool) {
	if strings.HasPrefix(ref, "${credential") {
		c.JSON(gohttp.StatusOK, gin.H{"error": "Managed credentials can't populate these options — use the signing-key fields"})
		return "", false
	}
	if strings.HasPrefix(ref, "${") {
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, ref)
		if errMsg != "" {
			c.JSON(gohttp.StatusOK, gin.H{"error": errMsg})
			return "", false
		}
		return resolved, true
	}
	return ref, true
}

// ociRequireDependency reads a dependency field (e.g. compartment_ocid) that
// later options are scoped to, prompting the operator to pick it first when it's
// blank or still an unresolved ${...} reference.
func (s *Service) ociRequireDependency(c *gin.Context, name, prompt string) (string, bool) {
	v := strings.TrimSpace(c.Query(name))
	if v == "" || strings.HasPrefix(v, "${") {
		c.JSON(gohttp.StatusOK, gin.H{"error": prompt})
		return "", false
	}
	return v, true
}

// ociOptErr summarises an OCI error without echoing raw request/signing material.
func ociOptErr(err error) string {
	if se, ok := common.IsServiceError(err); ok {
		return fmt.Sprintf("OCI rejected the request (%s): %s", se.GetCode(), se.GetMessage())
	}
	return "Could not reach OCI — check the region and credentials"
}

func (s *Service) getOracleCompartments(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	tenancy := strings.TrimSpace(c.Query("tenancy_ocid"))
	// The tenancy root isn't returned by ListCompartments — offer it explicitly.
	opts := []api.InputOption{{Name: "root (tenancy)", Value: tenancy}}
	subtree := true
	req := identity.ListCompartmentsRequest{CompartmentId: &tenancy, CompartmentIdInSubtree: &subtree, AccessLevel: identity.ListCompartmentsAccessLevelAny}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListCompartments(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			cp := &resp.Items[i]
			if cp.LifecycleState != identity.CompartmentLifecycleStateActive {
				continue
			}
			opts = append(opts, api.InputOption{Name: strDeref(cp.Name), Value: strDeref(cp.Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleAvailabilityDomains(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	// ADs are bounded per tenancy (typically 1–3), so this is a single call — no
	// pagination needed (deliberate, unlike the capped loops elsewhere in the file).
	resp, err := client.ListAvailabilityDomains(c.Request.Context(), identity.ListAvailabilityDomainsRequest{CompartmentId: &compartment})
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	opts := []api.InputOption{}
	for i := range resp.Items {
		opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].Name), Value: strDeref(resp.Items[i].Name)})
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleShapes(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := core.NewComputeClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	seen := map[string]bool{}
	opts := []api.InputOption{}
	req := core.ListShapesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListShapes(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			name := strDeref(resp.Items[i].Shape)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			opts = append(opts, api.InputOption{Name: name, Value: name})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleImages(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := core.NewComputeClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := core.ListImagesRequest{CompartmentId: &compartment}
	if shape := strings.TrimSpace(c.Query("shape")); shape != "" && !strings.HasPrefix(shape, "${") {
		req.Shape = &shape
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListImages(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].DisplayName), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleVcns(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := core.NewVirtualNetworkClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := core.ListVcnsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListVcns(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].DisplayName), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleSubnets(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := core.NewVirtualNetworkClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := core.ListSubnetsRequest{CompartmentId: &compartment}
	if vcn := strings.TrimSpace(c.Query("vcn_ocid")); vcn != "" && !strings.HasPrefix(vcn, "${") {
		req.VcnId = &vcn
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListSubnets(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].DisplayName), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
