package http

import (
	gohttp "net/http"

	"github.com/gin-gonic/gin"
	apigatewaysdk "github.com/oracle/oci-go-sdk/v65/apigateway"
	certssdk "github.com/oracle/oci-go-sdk/v65/certificatesmanagement"
	datacatalogsdk "github.com/oracle/oci-go-sdk/v65/datacatalog"
	dataflowsdk "github.com/oracle/oci-go-sdk/v65/dataflow"
	emailsdk "github.com/oracle/oci-go-sdk/v65/email"
	loggingsdk "github.com/oracle/oci-go-sdk/v65/logging"
	mysqlsdk "github.com/oracle/oci-go-sdk/v65/mysql"
	nosqlsdk "github.com/oracle/oci-go-sdk/v65/nosql"
	wafsdk "github.com/oracle/oci-go-sdk/v65/waf"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the 9 wave-3 Oracle Cloud nodes (Logging, API Gateway, WAF,
// Certificates, Email, NoSQL, MySQL, Data Flow, Data Catalog). Each node gets its compartment
// picker on every action, a primary-resource picker (compartment-scoped), and a
// destination-compartment picker on its change-compartment actions. Same shape as the sibling OCI
// proxies; 200 + {"error"} fallback; ACTIVE-filtered by stringified lifecycle where present.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")
	comp := "/api/v1/action/options/oracle-compartments"

	reg := func(node, id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/"+node+"/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	// Every action of every node gets the compartment picker.
	byNode := map[string][]string{
		"logging":      {"log_group_create", "log_group_get", "log_group_list", "log_group_update", "log_group_delete", "log_group_change_compartment", "log_create", "log_get", "log_list", "log_update", "log_delete", "log_change_log_group", "search_logs", "unified_agent_config_get", "unified_agent_config_list", "unified_agent_config_delete", "service_list"},
		"apigateway":   {"gateway_create", "gateway_get", "gateway_list", "gateway_update", "gateway_delete", "gateway_change_compartment", "deployment_create", "deployment_get", "deployment_list", "deployment_update", "deployment_delete", "deployment_change_compartment", "api_create", "api_get", "api_list", "api_update", "api_delete", "api_change_compartment"},
		"waf":          {"web_app_firewall_create", "web_app_firewall_get", "web_app_firewall_list", "web_app_firewall_update", "web_app_firewall_delete", "web_app_firewall_change_compartment", "policy_create", "policy_get", "policy_list", "policy_update", "policy_delete", "policy_change_compartment", "network_address_list_create", "network_address_list_get", "network_address_list_list", "network_address_list_delete", "protection_capability_list"},
		"certificates": {"certificate_create", "certificate_get", "certificate_list", "certificate_update", "certificate_schedule_deletion", "certificate_change_compartment", "certificate_authority_create", "certificate_authority_get", "certificate_authority_list", "certificate_authority_update", "certificate_authority_schedule_deletion", "ca_bundle_create", "ca_bundle_get", "ca_bundle_list", "ca_bundle_update", "ca_bundle_delete", "certificate_bundle_get", "ca_bundle_content_get", "association_list", "certificate_version_list"},
		"email":        {"email_domain_create", "email_domain_get", "email_domain_list", "email_domain_update", "email_domain_delete", "email_domain_change_compartment", "dkim_create", "dkim_get", "dkim_list", "dkim_delete", "sender_create", "sender_get", "sender_list", "sender_delete", "sender_change_compartment", "suppression_create", "suppression_get", "suppression_list", "suppression_delete"},
		"nosql":        {"table_create", "table_get", "table_list", "table_update", "table_delete", "table_change_compartment", "index_create", "index_get", "index_list", "index_delete", "row_get", "row_update", "row_delete", "query", "prepare_statement", "table_usage_list"},
		"mysql":        {"db_system_create", "db_system_get", "db_system_list", "db_system_update", "db_system_delete", "db_system_start", "db_system_stop", "db_system_change_compartment", "backup_create", "backup_get", "backup_list", "backup_delete", "configuration_get", "configuration_list", "heatwave_add", "heatwave_get", "heatwave_delete"},
		"dataflow":     {"application_create", "application_get", "application_list", "application_update", "application_delete", "application_change_compartment", "run_create", "run_get", "run_list", "run_update", "run_delete", "run_log_get", "private_endpoint_create", "private_endpoint_get", "private_endpoint_list", "private_endpoint_delete", "statement_create", "statement_get", "statement_list", "statement_delete"},
		"datacatalog":  {"catalog_create", "catalog_get", "catalog_list", "catalog_update", "catalog_delete", "catalog_change_compartment", "data_asset_create", "data_asset_get", "data_asset_list", "data_asset_update", "data_asset_delete", "connection_create", "connection_get", "connection_list", "connection_delete", "connection_test", "glossary_create", "glossary_get", "glossary_list", "glossary_delete", "term_create", "term_get", "term_list", "term_delete", "entity_get", "entity_list"},
	}
	for node, ids := range byNode {
		for _, id := range ids {
			reg(node, id, "compartment_ocid", comp, creds)
		}
	}

	// Primary-resource pickers + change-compartment destination pickers, per node.
	lg := "/api/v1/action/options/oracle-logging-log-groups"
	for _, id := range []string{"log_group_get", "log_group_update", "log_group_delete", "log_group_change_compartment", "log_create", "log_get", "log_list", "log_update", "log_delete", "log_change_log_group"} {
		reg("logging", id, "log_group_ocid", lg, credsComp)
	}
	reg("logging", "log_group_change_compartment", "destination_compartment_ocid", comp, creds)

	gw := "/api/v1/action/options/oracle-apigateway-gateways"
	for _, id := range []string{"gateway_get", "gateway_update", "gateway_delete", "gateway_change_compartment", "deployment_create", "deployment_list"} {
		reg("apigateway", id, "gateway_ocid", gw, credsComp)
	}
	for _, id := range []string{"gateway_change_compartment", "deployment_change_compartment", "api_change_compartment"} {
		reg("apigateway", id, "destination_compartment_ocid", comp, creds)
	}

	waf := "/api/v1/action/options/oracle-waf-firewalls"
	for _, id := range []string{"web_app_firewall_get", "web_app_firewall_update", "web_app_firewall_delete", "web_app_firewall_change_compartment"} {
		reg("waf", id, "web_app_firewall_ocid", waf, credsComp)
	}
	for _, id := range []string{"web_app_firewall_change_compartment", "policy_change_compartment"} {
		reg("waf", id, "destination_compartment_ocid", comp, creds)
	}

	certs := "/api/v1/action/options/oracle-certificates-certificates"
	for _, id := range []string{"certificate_get", "certificate_update", "certificate_schedule_deletion", "certificate_change_compartment", "certificate_version_list", "certificate_bundle_get"} {
		reg("certificates", id, "certificate_ocid", certs, credsComp)
	}
	reg("certificates", "certificate_change_compartment", "destination_compartment_ocid", comp, creds)

	dom := "/api/v1/action/options/oracle-email-domains"
	for _, id := range []string{"email_domain_get", "email_domain_update", "email_domain_delete", "email_domain_change_compartment", "dkim_create", "dkim_list"} {
		reg("email", id, "email_domain_ocid", dom, credsComp)
	}
	for _, id := range []string{"email_domain_change_compartment", "sender_change_compartment"} {
		reg("email", id, "destination_compartment_ocid", comp, creds)
	}

	tbl := "/api/v1/action/options/oracle-nosql-tables"
	for _, id := range []string{"table_get", "table_update", "table_delete", "table_change_compartment", "index_create", "index_get", "index_list", "index_delete", "row_get", "row_update", "row_delete", "table_usage_list"} {
		reg("nosql", id, "table_ocid_or_name", tbl, credsComp)
	}
	reg("nosql", "table_change_compartment", "destination_compartment_ocid", comp, creds)

	dbs := "/api/v1/action/options/oracle-mysql-db-systems"
	for _, id := range []string{"db_system_get", "db_system_update", "db_system_delete", "db_system_start", "db_system_stop", "db_system_change_compartment", "backup_create", "heatwave_add", "heatwave_get", "heatwave_delete"} {
		reg("mysql", id, "db_system_ocid", dbs, credsComp)
	}
	reg("mysql", "db_system_change_compartment", "destination_compartment_ocid", comp, creds)

	apps := "/api/v1/action/options/oracle-dataflow-applications"
	for _, id := range []string{"application_get", "application_update", "application_delete", "application_change_compartment", "run_create", "run_list"} {
		reg("dataflow", id, "application_ocid", apps, credsComp)
	}
	reg("dataflow", "application_change_compartment", "destination_compartment_ocid", comp, creds)

	cats := "/api/v1/action/options/oracle-datacatalog-catalogs"
	for _, id := range []string{"catalog_get", "catalog_update", "catalog_delete", "catalog_change_compartment", "data_asset_create", "data_asset_get", "data_asset_list", "data_asset_update", "data_asset_delete", "connection_create", "connection_get", "connection_list", "connection_delete", "connection_test", "glossary_create", "glossary_get", "glossary_list", "glossary_delete", "term_create", "term_get", "term_list", "term_delete", "entity_get", "entity_list"} {
		reg("datacatalog", id, "catalog_ocid", cats, credsComp)
	}
	reg("datacatalog", "catalog_change_compartment", "destination_compartment_ocid", comp, creds)
}

func (s *Service) getOracleLoggingLogGroups(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := loggingsdk.NewLoggingManagementClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := loggingsdk.ListLogGroupsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListLogGroups(c.Request.Context(), req)
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

func (s *Service) getOracleApiGatewayGateways(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := apigatewaysdk.NewGatewayClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := apigatewaysdk.ListGatewaysRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListGateways(c.Request.Context(), req)
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

func (s *Service) getOracleWafFirewalls(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := wafsdk.NewWafClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := wafsdk.ListWebAppFirewallsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListWebAppFirewalls(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].GetDisplayName()), Value: strDeref(resp.Items[i].GetId())})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleCertificatesCertificates(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := certssdk.NewCertificatesManagementClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := certssdk.ListCertificatesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListCertificates(c.Request.Context(), req)
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

func (s *Service) getOracleEmailDomains(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := emailsdk.NewEmailClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := emailsdk.ListEmailDomainsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListEmailDomains(c.Request.Context(), req)
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

func (s *Service) getOracleNosqlTables(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := nosqlsdk.NewNosqlClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := nosqlsdk.ListTablesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListTables(c.Request.Context(), req)
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

func (s *Service) getOracleMysqlDbSystems(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := mysqlsdk.NewDbSystemClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := mysqlsdk.ListDbSystemsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListDbSystems(c.Request.Context(), req)
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

func (s *Service) getOracleDataflowApplications(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := dataflowsdk.NewDataFlowClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dataflowsdk.ListApplicationsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListApplications(c.Request.Context(), req)
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

func (s *Service) getOracleDatacatalogCatalogs(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := datacatalogsdk.NewDataCatalogClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := datacatalogsdk.ListCatalogsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListCatalogs(c.Request.Context(), req)
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
