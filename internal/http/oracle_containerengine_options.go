package http

import (
	gohttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	okesdk "github.com/oracle/oci-go-sdk/v65/containerengine"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud Container Engine (OKE) node. Same shape
// as the sibling OCI proxies: build an OCI ConfigurationProvider from the node's connection
// (private key resolved server-side from ${secrets.X}), call the OKE list APIs, and return
// {options:[{name,value}]} — or an HTTP 200 + {"error":...} fallback. Pickers filter to
// ACTIVE so deleted/creating resources don't pollute the dropdowns.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	clustersEP := "/api/v1/action/options/oracle-oke-clusters"
	nodePoolsEP := "/api/v1/action/options/oracle-oke-node-pools"
	vnpEP := "/api/v1/action/options/oracle-oke-virtual-node-pools"

	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/containerengine/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	allActions := []string{
		"cluster_create", "cluster_get", "cluster_list", "cluster_update", "cluster_delete", "cluster_update_endpoint_config", "cluster_options_get", "kubeconfig_create",
		"node_pool_create", "node_pool_get", "node_pool_list", "node_pool_update", "node_pool_delete", "node_pool_options_get", "node_delete", "node_reboot", "node_replace_boot_volume",
		"virtual_node_pool_create", "virtual_node_pool_get", "virtual_node_pool_list", "virtual_node_pool_update", "virtual_node_pool_delete", "virtual_node_get", "virtual_node_list",
		"addon_install", "addon_get", "addon_list", "addon_update", "addon_disable", "addon_options_list",
		"work_request_get", "work_request_list", "work_request_errors_list", "work_request_logs_list", "work_request_cancel",
		"pod_shape_list",
		"workload_mapping_create", "workload_mapping_get", "workload_mapping_list", "workload_mapping_update", "workload_mapping_delete",
		"credential_rotation_start", "credential_rotation_complete", "credential_rotation_status_get",
	}
	for _, a := range allActions {
		reg(a, "compartment_ocid", comp, creds)
	}

	// cluster_ocid → the clusters picker on every action that targets or scopes to a cluster.
	for _, a := range []string{
		"cluster_get", "cluster_update", "cluster_delete", "cluster_update_endpoint_config", "kubeconfig_create",
		"node_pool_create", "node_pool_list", "node_reboot", "node_replace_boot_volume",
		"virtual_node_pool_create", "virtual_node_pool_list",
		"addon_install", "addon_get", "addon_list", "addon_update", "addon_disable",
		"work_request_list",
		"workload_mapping_create", "workload_mapping_get", "workload_mapping_list", "workload_mapping_update", "workload_mapping_delete",
		"credential_rotation_start", "credential_rotation_complete", "credential_rotation_status_get",
	} {
		reg(a, "cluster_ocid", clustersEP, credsComp)
	}

	// node_pool_ocid → the node-pools picker (compartment-scoped).
	for _, a := range []string{"node_pool_get", "node_pool_update", "node_pool_delete", "node_delete"} {
		reg(a, "node_pool_ocid", nodePoolsEP, credsComp)
	}

	// virtual_node_pool_ocid → the virtual-node-pools picker (compartment-scoped).
	for _, a := range []string{"virtual_node_pool_get", "virtual_node_pool_update", "virtual_node_pool_delete", "virtual_node_get", "virtual_node_list"} {
		reg(a, "virtual_node_pool_ocid", vnpEP, credsComp)
	}
}

func (s *Service) oracleOKEClient(c *gin.Context) (okesdk.ContainerEngineClient, bool) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return okesdk.ContainerEngineClient{}, false
	}
	client, err := okesdk.NewContainerEngineClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return okesdk.ContainerEngineClient{}, false
	}
	client.HTTPClient = ociOptionsHTTPClient
	return client, true
}

func (s *Service) getOracleOKEClusters(c *gin.Context) {
	client, ok := s.oracleOKEClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := okesdk.ListClustersRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListClusters(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != okesdk.ClusterLifecycleStateActive {
				continue
			}
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].Name), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleOKENodePools(c *gin.Context) {
	client, ok := s.oracleOKEClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := okesdk.ListNodePoolsRequest{CompartmentId: &compartment}
	if clusterID := strings.TrimSpace(c.Query("cluster_ocid")); clusterID != "" && !strings.HasPrefix(clusterID, "${") {
		req.ClusterId = &clusterID
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListNodePools(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != okesdk.NodePoolLifecycleStateActive {
				continue
			}
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].Name), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleOKEVirtualNodePools(c *gin.Context) {
	client, ok := s.oracleOKEClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := okesdk.ListVirtualNodePoolsRequest{CompartmentId: &compartment}
	if clusterID := strings.TrimSpace(c.Query("cluster_ocid")); clusterID != "" && !strings.HasPrefix(clusterID, "${") {
		req.ClusterId = &clusterID
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListVirtualNodePools(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != okesdk.VirtualNodePoolLifecycleStateActive {
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
