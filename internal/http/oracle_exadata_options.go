package http

import (
	gohttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	dbsdk "github.com/oracle/oci-go-sdk/v65/database"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud Exadata (Database Service on Dedicated
// Infrastructure) node. Same shape as the sibling OCI proxies: build an OCI Configuration-
// Provider from the node's connection (private key resolved server-side from ${secrets.X}),
// call the Database list APIs, and return {options:[{name,value}]} — or an HTTP 200 +
// {"error":...} fallback. Pickers filter to AVAILABLE.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	infraEP := "/api/v1/action/options/oracle-exadata-infrastructures"
	vmClusterEP := "/api/v1/action/options/oracle-exadata-vm-clusters"

	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/exadata/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	allActions := []string{
		"cloud_exadata_infrastructure_create", "cloud_exadata_infrastructure_get", "cloud_exadata_infrastructure_list",
		"cloud_exadata_infrastructure_update", "cloud_exadata_infrastructure_delete", "cloud_exadata_infrastructure_change_compartment",
		"cloud_exadata_infrastructure_add_storage", "cloud_exadata_infrastructure_unallocated_resources_get",
		"cloud_vm_cluster_create", "cloud_vm_cluster_get", "cloud_vm_cluster_list", "cloud_vm_cluster_update", "cloud_vm_cluster_delete",
		"cloud_vm_cluster_change_compartment", "cloud_vm_cluster_add_vm", "cloud_vm_cluster_remove_vm", "cloud_vm_cluster_iorm_config_get",
		"db_server_list", "db_server_get", "db_node_list", "db_node_get", "db_node_update", "db_node_action",
		"maintenance_run_create", "maintenance_run_get", "maintenance_run_list", "maintenance_run_update",
	}
	for _, a := range allActions {
		reg(a, "compartment_ocid", comp, creds)
	}

	// cloud_exadata_infrastructure_ocid → the infrastructures picker (compartment-scoped):
	// the infra ops themselves, plus actions that target/scope to a rack.
	for _, a := range []string{
		"cloud_exadata_infrastructure_get", "cloud_exadata_infrastructure_update", "cloud_exadata_infrastructure_delete",
		"cloud_exadata_infrastructure_change_compartment", "cloud_exadata_infrastructure_add_storage", "cloud_exadata_infrastructure_unallocated_resources_get",
		"cloud_vm_cluster_create", "cloud_vm_cluster_list", "db_server_list", "db_server_get",
	} {
		reg(a, "cloud_exadata_infrastructure_ocid", infraEP, credsComp)
	}

	// cloud_vm_cluster_ocid → the VM-clusters picker (compartment-scoped).
	for _, a := range []string{
		"cloud_vm_cluster_get", "cloud_vm_cluster_update", "cloud_vm_cluster_delete", "cloud_vm_cluster_change_compartment",
		"cloud_vm_cluster_add_vm", "cloud_vm_cluster_remove_vm", "cloud_vm_cluster_iorm_config_get",
	} {
		reg(a, "cloud_vm_cluster_ocid", vmClusterEP, credsComp)
	}
	// db_node_list names its VM-cluster input vm_cluster_ocid (it also accepts a DB system),
	// so its picker registers under that name, not cloud_vm_cluster_ocid.
	reg("db_node_list", "vm_cluster_ocid", vmClusterEP, credsComp)

	// change-compartment destination pickers.
	for _, a := range []string{"cloud_exadata_infrastructure_change_compartment", "cloud_vm_cluster_change_compartment"} {
		reg(a, "destination_compartment_ocid", comp, creds)
	}
}

func (s *Service) oracleExadataClient(c *gin.Context) (dbsdk.DatabaseClient, bool) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return dbsdk.DatabaseClient{}, false
	}
	client, err := dbsdk.NewDatabaseClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return dbsdk.DatabaseClient{}, false
	}
	client.HTTPClient = ociOptionsHTTPClient
	return client, true
}

func (s *Service) getOracleExadataInfrastructures(c *gin.Context) {
	client, ok := s.oracleExadataClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dbsdk.ListCloudExadataInfrastructuresRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListCloudExadataInfrastructures(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != dbsdk.CloudExadataInfrastructureSummaryLifecycleStateAvailable {
				continue
			}
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].DisplayName), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleExadataVmClusters(c *gin.Context) {
	client, ok := s.oracleExadataClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := dbsdk.ListCloudVmClustersRequest{CompartmentId: &compartment}
	if infra := strings.TrimSpace(c.Query("cloud_exadata_infrastructure_ocid")); infra != "" && !strings.HasPrefix(infra, "${") {
		req.CloudExadataInfrastructureId = &infra
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListCloudVmClusters(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != dbsdk.CloudVmClusterSummaryLifecycleStateAvailable {
				continue
			}
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].DisplayName), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}
