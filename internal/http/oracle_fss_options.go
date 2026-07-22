package http

import (
	gohttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	fsssdk "github.com/oracle/oci-go-sdk/v65/filestorage"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud File Storage node. Same shape as the
// DNS/IAM proxies: build an OCI ConfigurationProvider from the node's connection (private
// key resolved server-side from ${secrets.X}), call the File Storage list APIs, and
// return {options:[{name,value}]} — or an HTTP 200 + {"error":...} fallback.
//
// FSS quirk: file systems, mount targets, export sets, snapshot policies, replications
// and outbound connectors are AVAILABILITY-DOMAIN-scoped, so their pickers require both
// compartment_ocid AND availability_domain. Exports and snapshots are not AD-scoped
// (exports by compartment; snapshots by file system). Markers register in init() below.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")
	credsCompAD := append(append([]string{}, credsComp...), "availability_domain")
	credsCompFS := append(append([]string{}, credsComp...), "file_system_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	ad := "/api/v1/action/options/oracle-availability-domains"
	subnets := "/api/v1/action/options/oracle-subnets"
	fsEP := "/api/v1/action/options/oracle-fss-file-systems"
	mtEP := "/api/v1/action/options/oracle-fss-mount-targets"
	esEP := "/api/v1/action/options/oracle-fss-export-sets"
	exEP := "/api/v1/action/options/oracle-fss-exports"
	snEP := "/api/v1/action/options/oracle-fss-snapshots"
	spEP := "/api/v1/action/options/oracle-fss-snapshot-policies"
	repEP := "/api/v1/action/options/oracle-fss-replications"
	ocEP := "/api/v1/action/options/oracle-fss-outbound-connectors"

	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/filestorage/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	// compartment_ocid on every compartment-scoped action; availability_domain on the AD-scoped ones.
	compartmentScoped := []string{
		"file_system_create", "file_system_list", "file_system_get", "file_system_update", "file_system_delete", "file_system_change_compartment",
		"mount_target_create", "mount_target_list", "mount_target_get", "mount_target_update", "mount_target_delete", "mount_target_change_compartment", "mount_target_upgrade_shape", "mount_target_downgrade_shape",
		"export_create", "export_list", "export_get", "export_update", "export_delete",
		"export_set_list", "export_set_get", "export_set_update",
		"snapshot_create", "snapshot_list", "snapshot_get", "snapshot_update", "snapshot_delete",
		"snapshot_policy_create", "snapshot_policy_list", "snapshot_policy_get", "snapshot_policy_update", "snapshot_policy_delete",
		"replication_create", "replication_list", "replication_get", "replication_update", "replication_delete",
		"replication_target_list",
		"quota_rule_create", "quota_rule_list", "quota_rule_get", "quota_rule_update", "quota_rule_delete",
		"outbound_connector_create", "outbound_connector_list", "outbound_connector_get", "outbound_connector_update", "outbound_connector_delete",
	}
	for _, a := range compartmentScoped {
		reg(a, "compartment_ocid", comp, creds)
	}
	adScoped := []string{
		"file_system_create", "file_system_list", "file_system_get", "file_system_update", "file_system_delete",
		"mount_target_create", "mount_target_list", "mount_target_get", "mount_target_update", "mount_target_delete", "mount_target_upgrade_shape", "mount_target_downgrade_shape",
		"export_set_list", "export_set_get", "export_set_update",
		"snapshot_create", "snapshot_get", "snapshot_update", "snapshot_delete",
		"snapshot_policy_create", "snapshot_policy_list", "snapshot_policy_get", "snapshot_policy_update", "snapshot_policy_delete",
		"replication_create", "replication_list", "replication_get", "replication_update", "replication_delete",
		"replication_target_list",
		// quota_rule + export + snapshot_list carry an availability_domain purely to scope
		// their AD-requiring file-system / export-set pickers (the action itself ignores it).
		"quota_rule_create", "quota_rule_list", "quota_rule_get", "quota_rule_update", "quota_rule_delete",
		"snapshot_list", "export_create", "export_list",
		"outbound_connector_create", "outbound_connector_list", "outbound_connector_get", "outbound_connector_update", "outbound_connector_delete",
	}
	for _, a := range adScoped {
		reg(a, "availability_domain", ad, credsComp)
	}

	// file_system_ocid → the file-systems picker (AD-scoped) on every action that targets a file system.
	for _, a := range []string{"file_system_get", "file_system_update", "file_system_delete", "file_system_change_compartment", "snapshot_create", "snapshot_list", "export_create", "export_list", "quota_rule_create", "quota_rule_list", "quota_rule_get", "quota_rule_update", "quota_rule_delete"} {
		reg(a, "file_system_ocid", fsEP, credsCompAD)
	}
	reg("replication_create", "source_file_system_ocid", fsEP, credsCompAD)
	reg("replication_create", "target_file_system_ocid", fsEP, credsCompAD)

	// mount_target_ocid → mount-targets picker (AD-scoped).
	for _, a := range []string{"mount_target_get", "mount_target_update", "mount_target_delete", "mount_target_change_compartment", "mount_target_upgrade_shape", "mount_target_downgrade_shape"} {
		reg(a, "mount_target_ocid", mtEP, credsCompAD)
	}
	reg("mount_target_create", "subnet_ocid", subnets, credsComp)

	// export_set_ocid → export-sets picker; export_ocid → exports picker.
	for _, a := range []string{"export_set_get", "export_set_update", "export_create", "export_list"} {
		reg(a, "export_set_ocid", esEP, credsCompAD)
	}
	for _, a := range []string{"export_get", "export_update", "export_delete"} {
		reg(a, "export_ocid", exEP, credsComp)
	}

	// snapshot_ocid → snapshots picker (scoped by the chosen file system).
	for _, a := range []string{"snapshot_get", "snapshot_update", "snapshot_delete"} {
		reg(a, "snapshot_ocid", snEP, credsCompFS)
	}
	// snapshot_policy / replication / outbound-connector per-resource pickers.
	for _, a := range []string{"snapshot_policy_get", "snapshot_policy_update", "snapshot_policy_delete"} {
		reg(a, "snapshot_policy_ocid", spEP, credsCompAD)
	}
	reg("file_system_create", "source_snapshot_ocid", snEP, credsCompFS)
	for _, a := range []string{"replication_get", "replication_update", "replication_delete"} {
		reg(a, "replication_ocid", repEP, credsCompAD)
	}
	for _, a := range []string{"outbound_connector_get", "outbound_connector_update", "outbound_connector_delete"} {
		reg(a, "outbound_connector_ocid", ocEP, credsCompAD)
	}

	// change-compartment destination pickers.
	for _, a := range []string{"file_system_change_compartment", "mount_target_change_compartment"} {
		reg(a, "target_compartment_ocid", comp, creds)
	}
}

func (s *Service) oracleFssClient(c *gin.Context) (fsssdk.FileStorageClient, bool) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return fsssdk.FileStorageClient{}, false
	}
	client, err := fsssdk.NewFileStorageClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return fsssdk.FileStorageClient{}, false
	}
	client.HTTPClient = ociOptionsHTTPClient
	return client, true
}

func (s *Service) getOracleFssFileSystems(c *gin.Context) {
	client, ok := s.oracleFssClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	adom, ok := s.ociRequireDependency(c, "availability_domain", "Select an availability domain first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := fsssdk.ListFileSystemsRequest{CompartmentId: &compartment, AvailabilityDomain: &adom}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListFileSystems(c.Request.Context(), req)
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

func (s *Service) getOracleFssMountTargets(c *gin.Context) {
	client, ok := s.oracleFssClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	adom, ok := s.ociRequireDependency(c, "availability_domain", "Select an availability domain first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := fsssdk.ListMountTargetsRequest{CompartmentId: &compartment, AvailabilityDomain: &adom}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListMountTargets(c.Request.Context(), req)
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

func (s *Service) getOracleFssExportSets(c *gin.Context) {
	client, ok := s.oracleFssClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	adom, ok := s.ociRequireDependency(c, "availability_domain", "Select an availability domain first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := fsssdk.ListExportSetsRequest{CompartmentId: &compartment, AvailabilityDomain: &adom}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListExportSets(c.Request.Context(), req)
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

func (s *Service) getOracleFssExports(c *gin.Context) {
	client, ok := s.oracleFssClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := fsssdk.ListExportsRequest{CompartmentId: &compartment}
	// Narrow to a specific file system / export set when the action already has one
	// selected, so the picker isn't the whole compartment's exports in a large tenancy.
	if fs := strings.TrimSpace(c.Query("file_system_ocid")); fs != "" {
		req.FileSystemId = &fs
	}
	if es := strings.TrimSpace(c.Query("export_set_ocid")); es != "" {
		req.ExportSetId = &es
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListExports(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].Path), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleFssSnapshots(c *gin.Context) {
	client, ok := s.oracleFssClient(c)
	if !ok {
		return
	}
	fsID, ok := s.ociRequireDependency(c, "file_system_ocid", "Select a file system first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := fsssdk.ListSnapshotsRequest{FileSystemId: &fsID}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListSnapshots(c.Request.Context(), req)
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

func (s *Service) getOracleFssSnapshotPolicies(c *gin.Context) {
	client, ok := s.oracleFssClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	adom, ok := s.ociRequireDependency(c, "availability_domain", "Select an availability domain first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := fsssdk.ListFilesystemSnapshotPoliciesRequest{CompartmentId: &compartment, AvailabilityDomain: &adom}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListFilesystemSnapshotPolicies(c.Request.Context(), req)
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

func (s *Service) getOracleFssReplications(c *gin.Context) {
	client, ok := s.oracleFssClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	adom, ok := s.ociRequireDependency(c, "availability_domain", "Select an availability domain first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := fsssdk.ListReplicationsRequest{CompartmentId: &compartment, AvailabilityDomain: &adom}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListReplications(c.Request.Context(), req)
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

func (s *Service) getOracleFssOutboundConnectors(c *gin.Context) {
	client, ok := s.oracleFssClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	adom, ok := s.ociRequireDependency(c, "availability_domain", "Select an availability domain first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := fsssdk.ListOutboundConnectorsRequest{CompartmentId: &compartment, AvailabilityDomain: &adom}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListOutboundConnectors(c.Request.Context(), req)
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
