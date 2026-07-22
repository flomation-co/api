package http

import (
	gohttp "net/http"

	"github.com/gin-gonic/gin"
	dnssdk "github.com/oracle/oci-go-sdk/v65/dns"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud DNS node. Same shape as the
// Compute/Networking/NLB proxies: build an OCI ConfigurationProvider from the node's
// connection (the private key resolved server-side from ${secrets.X}), call the DNS
// list APIs, and return {options:[{name,value}]} — or an HTTP 200 + {"error":...} so
// the editor shows the message inline and falls back to manual entry.
//
// The dynamicOptionsMetadata markers are registered in the init() below (a second
// init in this package — Go runs them all). DNS resources are keyed by OCID, except
// resolver endpoints which are keyed by NAME within their parent resolver.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")
	credsResolver := append(append([]string{}, creds...), "resolver_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	zonesEP := "/api/v1/action/options/oracle-dns-zones"
	policiesEP := "/api/v1/action/options/oracle-dns-steering-policies"
	attachmentsEP := "/api/v1/action/options/oracle-dns-steering-policy-attachments"
	viewsEP := "/api/v1/action/options/oracle-dns-views"
	resolversEP := "/api/v1/action/options/oracle-dns-resolvers"
	endpointsEP := "/api/v1/action/options/oracle-dns-resolver-endpoints"
	tsigEP := "/api/v1/action/options/oracle-dns-tsig-keys"

	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/dns/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	// compartment_ocid → the shared compartment picker on every compartment-scoped
	// action (creates + lists). Per-resource actions also carry compartment_ocid to
	// scope THEIR resource picker below, so register it there too.
	compartmentScoped := []string{
		"zone_create", "zone_list", "zone_create_from_file",
		"steering_policy_create", "steering_policy_list",
		"steering_policy_attachment_create", "steering_policy_attachment_list",
		"tsig_key_create", "tsig_key_list",
		"view_create", "view_list",
		"resolver_list",
	}
	for _, a := range compartmentScoped {
		reg(a, "compartment_ocid", comp, creds)
	}

	// zone_name_or_ocid → the zones picker (scoped by compartment) on every zone and
	// record action. The picker's value is the zone OCID, which the name-or-id path
	// accepts. Record actions live under the zone, so they scope by compartment too.
	zoneKeyed := []string{
		"zone_get", "zone_update", "zone_delete", "zone_change_compartment",
		"zone_get_content", "zone_list_transfer_servers",
		"zone_records_get", "zone_records_patch", "zone_records_update",
		"rrset_get", "rrset_update", "rrset_patch", "rrset_delete",
		"domain_records_get", "domain_records_patch", "domain_records_update", "domain_records_delete",
	}
	for _, a := range zoneKeyed {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "zone_name_or_ocid", zonesEP, credsComp)
	}
	// DNSSEC ops key on the zone OCID (a different input name) but pick the same list.
	for _, a := range []string{"zone_dnssec_stage_key_version", "zone_dnssec_promote_key_version"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "zone_ocid", zonesEP, credsComp)
	}
	// view_ocid on zone_create / zone_create_from_file → the private-DNS view picker.
	for _, a := range []string{"zone_create", "zone_create_from_file"} {
		reg(a, "view_ocid", viewsEP, credsComp)
	}

	// steering_policy_ocid → the policies picker on the per-policy actions plus the
	// attachment create/list that reference a policy.
	for _, a := range []string{"steering_policy_get", "steering_policy_update", "steering_policy_delete", "steering_policy_change_compartment", "steering_policy_attachment_create", "steering_policy_attachment_list"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "steering_policy_ocid", policiesEP, credsComp)
	}
	// zone_ocid on attachment create/list → the zones picker.
	for _, a := range []string{"steering_policy_attachment_create", "steering_policy_attachment_list"} {
		reg(a, "zone_ocid", zonesEP, credsComp)
	}
	// attachment_ocid → the attachments picker on the per-attachment actions.
	for _, a := range []string{"steering_policy_attachment_get", "steering_policy_attachment_update", "steering_policy_attachment_delete"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "attachment_ocid", attachmentsEP, credsComp)
	}

	// tsig_key_ocid → the TSIG-keys picker on the per-key actions.
	for _, a := range []string{"tsig_key_get", "tsig_key_update", "tsig_key_delete", "tsig_key_change_compartment"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "tsig_key_ocid", tsigEP, credsComp)
	}

	// view_ocid → the views picker on the per-view actions.
	for _, a := range []string{"view_get", "view_update", "view_delete", "view_change_compartment"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "view_ocid", viewsEP, credsComp)
	}

	// resolver_ocid → the resolvers picker on the per-resolver actions and every
	// resolver-endpoint action (which lives under a resolver).
	for _, a := range []string{"resolver_get", "resolver_update", "resolver_change_compartment", "resolver_endpoint_create", "resolver_endpoint_get", "resolver_endpoint_list", "resolver_endpoint_update", "resolver_endpoint_delete"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "resolver_ocid", resolversEP, credsComp)
	}
	// endpoint_name → the resolver-endpoints picker (scoped by the chosen resolver).
	for _, a := range []string{"resolver_endpoint_get", "resolver_endpoint_update", "resolver_endpoint_delete"} {
		reg(a, "endpoint_name", endpointsEP, credsResolver)
	}

	// target_compartment_ocid on every change-compartment action → the compartment picker.
	for _, a := range []string{"zone_change_compartment", "steering_policy_change_compartment", "tsig_key_change_compartment", "view_change_compartment", "resolver_change_compartment"} {
		reg(a, "target_compartment_ocid", comp, creds)
	}
}

// oracleDnsClient builds a DNS client from the node's connection, writing the graceful
// error and returning ok=false on any problem.
func (s *Service) oracleDnsClient(c *gin.Context) (dnssdk.DnsClient, bool) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return dnssdk.DnsClient{}, false
	}
	client, err := dnssdk.NewDnsClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return dnssdk.DnsClient{}, false
	}
	client.HTTPClient = ociOptionsHTTPClient
	return client, true
}

func (s *Service) getOracleDnsZones(c *gin.Context) {
	client, ok := s.oracleDnsClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dnssdk.ListZonesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListZones(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].Name), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleDnsSteeringPolicies(c *gin.Context) {
	client, ok := s.oracleDnsClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dnssdk.ListSteeringPoliciesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListSteeringPolicies(c.Request.Context(), req)
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

func (s *Service) getOracleDnsSteeringPolicyAttachments(c *gin.Context) {
	client, ok := s.oracleDnsClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dnssdk.ListSteeringPolicyAttachmentsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListSteeringPolicyAttachments(c.Request.Context(), req)
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

func (s *Service) getOracleDnsViews(c *gin.Context) {
	client, ok := s.oracleDnsClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dnssdk.ListViewsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListViews(c.Request.Context(), req)
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

func (s *Service) getOracleDnsResolvers(c *gin.Context) {
	client, ok := s.oracleDnsClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dnssdk.ListResolversRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListResolvers(c.Request.Context(), req)
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

func (s *Service) getOracleDnsTsigKeys(c *gin.Context) {
	client, ok := s.oracleDnsClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dnssdk.ListTsigKeysRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListTsigKeys(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].Name), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

// getOracleDnsResolverEndpoints lists a resolver's endpoints for the endpoint_name
// picker. Endpoints are keyed by NAME within the resolver, so the option value is the
// name (the ResolverEndpointSummary is a polymorphic interface — read via getters).
func (s *Service) getOracleDnsResolverEndpoints(c *gin.Context) {
	client, ok := s.oracleDnsClient(c)
	if !ok {
		return
	}
	resolverID, ok := s.ociRequireDependency(c, "resolver_ocid", "Select a resolver first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dnssdk.ListResolverEndpointsRequest{ResolverId: &resolverID}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListResolverEndpoints(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for _, item := range resp.Items {
			name := strDeref(item.GetName())
			opts = append(opts, api.InputOption{Name: name, Value: name})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}
