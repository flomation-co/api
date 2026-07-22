package http

import (
	gohttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	identity "github.com/oracle/oci-go-sdk/v65/identity"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud Identity (IAM) node. Same shape as
// the DNS/compute proxies: build an OCI ConfigurationProvider from the node's connection
// (the private key resolved server-side from ${secrets.X}), call the Identity list APIs,
// and return {options:[{name,value}]} — or an HTTP 200 + {"error":...} fallback.
//
// IAM resources (users/groups/policies/dynamic groups) live in the TENANCY root, so the
// pickers default a blank compartment_ocid to the tenancy, matching the executor's
// CompartmentOrTenancy. The dynamicOptionsMetadata markers register in init() below.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")
	credsProvider := append(append([]string{}, creds...), "identity_provider_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	usersEP := "/api/v1/action/options/oracle-iam-users"
	groupsEP := "/api/v1/action/options/oracle-iam-groups"
	policiesEP := "/api/v1/action/options/oracle-iam-policies"
	dynGroupsEP := "/api/v1/action/options/oracle-iam-dynamic-groups"
	netSourcesEP := "/api/v1/action/options/oracle-iam-network-sources"
	tagNamespacesEP := "/api/v1/action/options/oracle-iam-tag-namespaces"
	providersEP := "/api/v1/action/options/oracle-iam-identity-providers"

	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/identity/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	// compartment_ocid → the shared compartment picker on every compartment-scoped action.
	compartmentScoped := []string{
		"user_create", "user_list", "group_create", "group_list", "policy_create", "policy_list",
		"compartment_create", "compartment_list", "dynamic_group_create", "dynamic_group_list",
		"user_add_to_group", "user_get", "user_delete", "user_update", "user_update_state",
		"user_update_capabilities", "user_create_or_reset_ui_password", "user_get_ui_password_info",
		"auth_token_create", "membership_list",
		"network_source_create", "network_source_list", "tag_namespace_create", "tag_namespace_list",
		"identity_provider_create", "identity_provider_list", "authentication_policy_get",
		"authentication_policy_update", "availability_domain_list",
	}
	for _, a := range compartmentScoped {
		reg(a, "compartment_ocid", comp, creds)
	}

	// target_user_ocid → the users picker on every action that references a user (memberships,
	// credentials, tokens, keys, MFA, UI password). All scoped by compartment_ocid.
	userScoped := []string{
		"user_get", "user_delete", "user_update", "user_update_state", "user_update_capabilities",
		"user_create_or_reset_ui_password", "user_get_ui_password_info", "user_add_to_group", "membership_list",
		"auth_token_create", "auth_token_list", "auth_token_update", "auth_token_delete",
		"api_key_upload", "api_key_list", "api_key_delete",
		"smtp_credential_create", "smtp_credential_list", "smtp_credential_delete",
		"customer_secret_key_create", "customer_secret_key_list", "customer_secret_key_delete",
		"oauth_credential_create", "oauth_credential_list", "oauth_credential_delete",
		"db_credential_create", "db_credential_list", "db_credential_delete",
		"swift_password_create", "swift_password_list", "swift_password_delete",
		"mfa_totp_create", "mfa_totp_get", "mfa_totp_list", "mfa_totp_delete", "mfa_totp_activate",
	}
	for _, a := range userScoped {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "target_user_ocid", usersEP, credsComp)
	}

	// group_ocid → the groups picker (membership create, IdP group mapping create).
	for _, a := range []string{"group_get", "group_update", "group_delete", "user_add_to_group", "membership_list", "idp_group_mapping_create"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "group_ocid", groupsEP, credsComp)
	}
	// policy_ocid → the policies picker.
	for _, a := range []string{"policy_get", "policy_update", "policy_delete"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "policy_ocid", policiesEP, credsComp)
	}
	// dynamic_group_ocid → the dynamic-groups picker.
	for _, a := range []string{"dynamic_group_get", "dynamic_group_update", "dynamic_group_delete"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "dynamic_group_ocid", dynGroupsEP, credsComp)
	}
	// network_source_ocid → the network-sources picker.
	for _, a := range []string{"network_source_get", "network_source_update", "network_source_delete"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "network_source_ocid", netSourcesEP, credsComp)
	}
	// tag_namespace_ocid → the tag-namespaces picker (namespace ops + tag-key ops under it).
	for _, a := range []string{"tag_namespace_get", "tag_namespace_update", "tag_namespace_delete", "tag_create", "tag_get", "tag_list", "tag_update", "tag_delete"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "tag_namespace_ocid", tagNamespacesEP, credsComp)
	}
	// identity_provider_ocid → the identity-providers picker (provider ops + group mappings).
	for _, a := range []string{"identity_provider_get", "identity_provider_delete", "idp_group_mapping_create", "idp_group_mapping_list", "idp_group_mapping_delete"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "identity_provider_ocid", providersEP, credsComp)
	}
	// group_ocid on idp_group_mapping_create uses the groups picker too (registered above).
	_ = credsProvider

	// Compartment actions: the resource + destination pickers.
	for _, a := range []string{"compartment_get", "compartment_update", "compartment_delete", "compartment_move", "compartment_recover"} {
		reg(a, "compartment_ocid", comp, creds)
		reg(a, "target_compartment_ocid", comp, creds)
	}
	reg("compartment_move", "destination_compartment_ocid", comp, creds)
}

// oracleIamClient builds an Identity client from the node's connection; on any problem it
// writes the graceful error and returns ok=false.
func (s *Service) oracleIamClient(c *gin.Context) (identity.IdentityClient, bool) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return identity.IdentityClient{}, false
	}
	client, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return identity.IdentityClient{}, false
	}
	client.HTTPClient = ociOptionsHTTPClient
	return client, true
}

// iamCompartment resolves the compartment for a picker: the supplied compartment_ocid, or
// the tenancy root when blank (IAM resources live in the tenancy).
func iamCompartment(c *gin.Context) string {
	if v := strings.TrimSpace(c.Query("compartment_ocid")); v != "" {
		return v
	}
	return strings.TrimSpace(c.Query("tenancy_ocid"))
}

func (s *Service) getOracleIamUsers(c *gin.Context) {
	client, ok := s.oracleIamClient(c)
	if !ok {
		return
	}
	compartment := iamCompartment(c)
	opts := []api.InputOption{}
	req := identity.ListUsersRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListUsers(c.Request.Context(), req)
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

func (s *Service) getOracleIamGroups(c *gin.Context) {
	client, ok := s.oracleIamClient(c)
	if !ok {
		return
	}
	compartment := iamCompartment(c)
	opts := []api.InputOption{}
	req := identity.ListGroupsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListGroups(c.Request.Context(), req)
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

func (s *Service) getOracleIamPolicies(c *gin.Context) {
	client, ok := s.oracleIamClient(c)
	if !ok {
		return
	}
	compartment := iamCompartment(c)
	opts := []api.InputOption{}
	req := identity.ListPoliciesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListPolicies(c.Request.Context(), req)
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

func (s *Service) getOracleIamDynamicGroups(c *gin.Context) {
	client, ok := s.oracleIamClient(c)
	if !ok {
		return
	}
	compartment := iamCompartment(c)
	opts := []api.InputOption{}
	req := identity.ListDynamicGroupsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListDynamicGroups(c.Request.Context(), req)
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

func (s *Service) getOracleIamNetworkSources(c *gin.Context) {
	client, ok := s.oracleIamClient(c)
	if !ok {
		return
	}
	compartment := iamCompartment(c)
	opts := []api.InputOption{}
	req := identity.ListNetworkSourcesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListNetworkSources(c.Request.Context(), req)
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

func (s *Service) getOracleIamTagNamespaces(c *gin.Context) {
	client, ok := s.oracleIamClient(c)
	if !ok {
		return
	}
	compartment := iamCompartment(c)
	opts := []api.InputOption{}
	req := identity.ListTagNamespacesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListTagNamespaces(c.Request.Context(), req)
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

func (s *Service) getOracleIamIdentityProviders(c *gin.Context) {
	client, ok := s.oracleIamClient(c)
	if !ok {
		return
	}
	compartment := iamCompartment(c)
	opts := []api.InputOption{}
	req := identity.ListIdentityProvidersRequest{CompartmentId: &compartment, Protocol: identity.ListIdentityProvidersProtocolSaml2}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListIdentityProviders(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].GetName()), Value: strDeref(resp.Items[i].GetId())})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}
