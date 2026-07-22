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

	// Networking: reuse the existing compartment + VCN pickers across the write
	// surface. compartment_ocid on every create + list; the destination on the
	// move actions; vcn_ocid on every create that lives in a VCN and the optional
	// VCN filter on the lists; and the NEW route-table picker on the create forms
	// that reference a route table (all of which carry compartment + vcn to scope
	// it). Per-resource ops (get/update/delete on a resource, NSG rule actions)
	// take a plain OCID — like Compute's instance_ocid, they have no compartment
	// field to scope a list.
	netReg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/networking/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}
	netResources := []string{"vcn", "subnet", "security_list", "route_table", "internet_gateway", "nat_gateway", "service_gateway", "nsg", "dhcp_options", "public_ip"}
	for _, r := range netResources {
		netReg(r+"_create", "compartment_ocid", "/api/v1/action/options/oracle-compartments", creds)
		netReg(r+"_get_all", "compartment_ocid", "/api/v1/action/options/oracle-compartments", creds)
	}
	for _, r := range []string{"vcn", "subnet", "security_list", "route_table", "internet_gateway", "nat_gateway", "nsg"} {
		netReg(r+"_change_compartment", "target_compartment_ocid", "/api/v1/action/options/oracle-compartments", creds)
	}
	// vcn_ocid: on the VCN-scoped creates and the optional list filters.
	for _, r := range []string{"subnet", "security_list", "route_table", "internet_gateway", "nat_gateway", "service_gateway", "nsg", "dhcp_options"} {
		netReg(r+"_create", "vcn_ocid", "/api/v1/action/options/oracle-vcns", credsComp)
		netReg(r+"_get_all", "vcn_ocid", "/api/v1/action/options/oracle-vcns", credsComp)
	}
	// route_table_ocid: the NEW oracle-route-tables picker (compartment + vcn scoped).
	for _, id := range []string{"subnet_create", "internet_gateway_create", "nat_gateway_create", "service_gateway_create"} {
		netReg(id, "route_table_ocid", "/api/v1/action/options/oracle-route-tables", credsCompVCN)
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

	// Block Volumes: pickers for every identity-picked resource, each scoped by the
	// compartment_ocid present on that action (which is why per-resource get/update/
	// delete actions carry a compartment field — it scopes their volume/policy
	// picker, unlike Compute's instance_ocid which had none). Backups, replicas and
	// attachments key on an OCID normally chained from an upstream node's output, so
	// they stay plain text; asset_ocid is polymorphic (volume OR boot volume) so it
	// also stays plain. The private_key etc. remain in `creds`.
	credsCompAD := append(append([]string{}, credsComp...), "availability_domain")
	bvReg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/blockvolume/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}
	comp := "/api/v1/action/options/oracle-compartments"
	adEP := "/api/v1/action/options/oracle-availability-domains"
	volEP := "/api/v1/action/options/oracle-volumes"
	bootVolEP := "/api/v1/action/options/oracle-boot-volumes"
	groupEP := "/api/v1/action/options/oracle-volume-groups"
	instEP := "/api/v1/action/options/oracle-instances"
	policyEP := "/api/v1/action/options/oracle-backup-policies"

	// compartment_ocid on every Block Volumes action that has one (creds-only).
	bvCompartmentActions := []string{
		"volume_create", "volume_get", "volume_get_all", "volume_update", "volume_delete", "volume_change_compartment", "volume_attach",
		"volume_backup_create", "volume_backup_get", "volume_backup_get_all", "volume_backup_update", "volume_backup_delete", "volume_backup_change_compartment",
		"boot_volume_create", "boot_volume_get", "boot_volume_get_all", "boot_volume_update", "boot_volume_delete", "boot_volume_change_compartment",
		"boot_volume_backup_create", "boot_volume_backup_get", "boot_volume_backup_get_all", "boot_volume_backup_update", "boot_volume_backup_delete", "boot_volume_backup_change_compartment",
		"volume_group_create", "volume_group_get", "volume_group_get_all", "volume_group_update", "volume_group_delete", "volume_group_change_compartment",
		"volume_group_backup_create", "volume_group_backup_get", "volume_group_backup_get_all", "volume_group_backup_update", "volume_group_backup_delete", "volume_group_backup_change_compartment",
		"backup_policy_create", "backup_policy_get", "backup_policy_get_all", "backup_policy_update", "backup_policy_delete", "backup_policy_assign", "backup_policy_get_assignment", "backup_policy_unassign",
		"volume_kms_key_get", "volume_kms_key_update", "volume_kms_key_delete",
		"boot_volume_kms_key_get", "boot_volume_kms_key_update", "boot_volume_kms_key_delete",
		"volume_replica_get", "volume_replica_get_all", "boot_volume_replica_get", "boot_volume_replica_get_all", "volume_group_replica_get", "volume_group_replica_get_all",
		"volume_attachment_get_all", "volume_attachment_update", "boot_volume_attachment_attach", "boot_volume_attachment_get_all",
	}
	for _, a := range bvCompartmentActions {
		bvReg(a, "compartment_ocid", comp, creds)
	}
	// destination_compartment_ocid on the six move actions (real target compartment).
	for _, a := range []string{"volume_change_compartment", "volume_backup_change_compartment", "boot_volume_change_compartment", "boot_volume_backup_change_compartment", "volume_group_change_compartment", "volume_group_backup_change_compartment"} {
		bvReg(a, "destination_compartment_ocid", comp, creds)
	}
	// availability_domain (compartment-scoped).
	for _, a := range []string{"volume_create", "volume_get_all", "boot_volume_create", "boot_volume_get_all", "volume_group_create", "volume_group_get_all", "volume_replica_get_all", "boot_volume_replica_get_all", "volume_group_replica_get_all", "boot_volume_attachment_get_all"} {
		bvReg(a, "availability_domain", adEP, credsComp)
	}
	// volume_ocid → block-volume picker (compartment + optional AD scoped).
	for _, a := range []string{"volume_get", "volume_update", "volume_delete", "volume_change_compartment", "volume_attach", "volume_backup_create", "volume_kms_key_get", "volume_kms_key_update", "volume_kms_key_delete", "volume_backup_get_all", "volume_attachment_get_all"} {
		bvReg(a, "volume_ocid", volEP, credsCompAD)
	}
	// boot_volume_ocid → boot-volume picker.
	for _, a := range []string{"boot_volume_get", "boot_volume_update", "boot_volume_delete", "boot_volume_change_compartment", "boot_volume_backup_create", "boot_volume_kms_key_get", "boot_volume_kms_key_update", "boot_volume_kms_key_delete", "boot_volume_attachment_attach", "boot_volume_backup_get_all", "boot_volume_attachment_get_all"} {
		bvReg(a, "boot_volume_ocid", bootVolEP, credsCompAD)
	}
	// volume_group_ocid → volume-group picker (+ the optional list filter uses volume_group_id).
	for _, a := range []string{"volume_group_get", "volume_group_update", "volume_group_delete", "volume_group_change_compartment", "volume_group_backup_create"} {
		bvReg(a, "volume_group_ocid", groupEP, credsCompAD)
	}
	bvReg("volume_group_backup_get_all", "volume_group_id", groupEP, credsCompAD)
	// instance_ocid → compute-instance picker on the attach actions + list filters.
	for _, a := range []string{"volume_attach", "volume_attachment_get_all", "boot_volume_attachment_attach", "boot_volume_attachment_get_all"} {
		bvReg(a, "instance_ocid", instEP, credsComp)
	}
	// policy_ocid → backup-policy picker. Forward the action's compartment so the
	// operator's own user-defined policies appear (update/delete can only target
	// those); with no compartment the handler still returns the predefined
	// Bronze/Silver/Gold/Platinum set, since it treats compartment_ocid as optional.
	for _, a := range []string{"backup_policy_assign", "backup_policy_get", "backup_policy_update", "backup_policy_delete"} {
		bvReg(a, "policy_ocid", policyEP, credsComp)
	}
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

// getOracleRouteTables lists the route tables in a compartment (optionally scoped
// to a VCN) for the route_table_ocid picker on the create forms. Mirrors
// getOracleSubnets — compartment required, VCN optional.
func (s *Service) getOracleRouteTables(c *gin.Context) {
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
	req := core.ListRouteTablesRequest{CompartmentId: &compartment}
	if vcn := strings.TrimSpace(c.Query("vcn_ocid")); vcn != "" && !strings.HasPrefix(vcn, "${") {
		req.VcnId = &vcn
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListRouteTables(c.Request.Context(), req)
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

// getOracleVolumes lists the block volumes in a compartment for the volume_ocid /
// asset_ocid pickers across the Block Volumes node. Mirrors getOracleVcns:
// compartment required, paginated under the shared cap.
func (s *Service) getOracleVolumes(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := core.NewBlockstorageClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := core.ListVolumesRequest{CompartmentId: &compartment}
	if ad := strings.TrimSpace(c.Query("availability_domain")); ad != "" && !strings.HasPrefix(ad, "${") {
		req.AvailabilityDomain = &ad
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListVolumes(c.Request.Context(), req)
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

// getOracleBootVolumes lists the boot volumes in a compartment for the
// boot_volume_ocid picker across the boot-volume actions. Boot volumes are
// compartment-scoped; the availability domain is an optional narrower filter.
func (s *Service) getOracleBootVolumes(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := core.NewBlockstorageClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := core.ListBootVolumesRequest{CompartmentId: &compartment}
	if ad := strings.TrimSpace(c.Query("availability_domain")); ad != "" && !strings.HasPrefix(ad, "${") {
		req.AvailabilityDomain = &ad
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListBootVolumes(c.Request.Context(), req)
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

// getOracleVolumeGroups lists the volume groups in a compartment for the
// volume_group_ocid picker across the volume-group actions.
func (s *Service) getOracleVolumeGroups(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := core.NewBlockstorageClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := core.ListVolumeGroupsRequest{CompartmentId: &compartment}
	if ad := strings.TrimSpace(c.Query("availability_domain")); ad != "" && !strings.HasPrefix(ad, "${") {
		req.AvailabilityDomain = &ad
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListVolumeGroups(c.Request.Context(), req)
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

// getOracleInstances lists the compute instances in a compartment for the
// instance_ocid picker on the attach actions (a volume attaches to an instance).
func (s *Service) getOracleInstances(c *gin.Context) {
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
	req := core.ListInstancesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListInstances(c.Request.Context(), req)
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

// getOracleBackupPolicies lists the backup policies available to assign. The
// compartment is OPTIONAL here — with none, OCI returns its predefined
// Bronze/Silver/Gold/Platinum policies (tenancy-wide); with one, the user-defined
// policies in it. So this proxy does NOT require a compartment dependency.
func (s *Service) getOracleBackupPolicies(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := core.NewBlockstorageClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := core.ListVolumeBackupPoliciesRequest{}
	if comp := strings.TrimSpace(c.Query("compartment_ocid")); comp != "" && !strings.HasPrefix(comp, "${") {
		req.CompartmentId = &comp
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListVolumeBackupPolicies(c.Request.Context(), req)
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
