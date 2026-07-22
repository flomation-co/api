package http

import (
	gohttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	keymanagement "github.com/oracle/oci-go-sdk/v65/keymanagement"
	ovault "github.com/oracle/oci-go-sdk/v65/vault"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud Vault / KMS node. Same shape as the
// DNS/IAM/FSS proxies: build an OCI ConfigurationProvider from the node's connection
// (private key resolved server-side from ${secrets.X}), call the KMS/Vault list APIs, and
// return {options:[{name,value}]} — or an HTTP 200 + {"error":...} fallback.
//
// KMS quirk: keys and key versions live at the VAULT's own management endpoint, not the
// regional one. So the key / key-version pickers are TWO-HOP — resolve the selected vault
// (GetVault) to read its ManagementEndpoint, build a KmsManagementClient pointed there, and
// only then list. Vaults and secrets use regional clients.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")
	credsCompVault := append(append([]string{}, credsComp...), "vault_ocid")
	credsCompVaultKey := append(append([]string{}, credsCompVault...), "key_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	vaultsEP := "/api/v1/action/options/oracle-vault-vaults"
	keysEP := "/api/v1/action/options/oracle-vault-keys"
	keyVersionsEP := "/api/v1/action/options/oracle-vault-key-versions"
	secretsEP := "/api/v1/action/options/oracle-vault-secrets"

	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/vault/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	// compartment_ocid picker on every action (each carries the compartment input, required or
	// as a picker-scope). Registering it everywhere keeps the operator on dropdowns throughout.
	allActions := []string{
		"vault_create", "vault_get", "vault_list", "vault_update", "vault_schedule_deletion", "vault_cancel_deletion",
		"vault_change_compartment", "vault_get_usage", "vault_list_replicas", "vault_create_replica", "vault_delete_replica",
		"vault_backup", "vault_restore",
		"key_create", "key_get", "key_list", "key_update", "key_enable", "key_disable", "key_schedule_deletion",
		"key_cancel_deletion", "key_change_compartment", "key_backup", "key_restore", "wrapping_key_get",
		"key_version_create", "key_version_get", "key_version_list", "key_version_schedule_deletion", "key_version_cancel_deletion",
		"encrypt", "decrypt", "generate_data_encryption_key", "sign", "verify", "export_key",
		"secret_create", "secret_get", "secret_list", "secret_update", "secret_schedule_deletion", "secret_cancel_deletion",
		"secret_change_compartment", "secret_version_get", "secret_version_list", "secret_version_schedule_deletion",
		"secret_version_cancel_deletion", "secret_bundle_get", "secret_bundle_get_by_name", "secret_bundle_versions_list",
	}
	for _, a := range allActions {
		reg(a, "compartment_ocid", comp, creds)
	}

	// vault_ocid → the vaults picker (compartment-scoped), on every action that targets a vault
	// or resolves a per-vault endpoint. (vault_list / vault_restore make/scan without a vault OCID.)
	vaultScoped := []string{
		"vault_get", "vault_update", "vault_schedule_deletion", "vault_cancel_deletion", "vault_change_compartment",
		"vault_get_usage", "vault_list_replicas", "vault_create_replica", "vault_delete_replica", "vault_backup",
		"key_create", "key_get", "key_list", "key_update", "key_enable", "key_disable", "key_schedule_deletion",
		"key_cancel_deletion", "key_change_compartment", "key_backup", "key_restore", "wrapping_key_get",
		"key_version_create", "key_version_get", "key_version_list", "key_version_schedule_deletion", "key_version_cancel_deletion",
		"encrypt", "decrypt", "generate_data_encryption_key", "sign", "verify", "export_key",
		"secret_create", "secret_list", "secret_bundle_get", "secret_bundle_get_by_name",
	}
	for _, a := range vaultScoped {
		reg(a, "vault_ocid", vaultsEP, credsComp)
	}

	// key_ocid → the two-hop keys picker (compartment + vault). On key ops, crypto ops, key
	// versions, and secret_create (whose key protects the secret).
	keyScoped := []string{
		"key_get", "key_update", "key_enable", "key_disable", "key_schedule_deletion", "key_cancel_deletion",
		"key_change_compartment", "key_backup",
		"key_version_create", "key_version_get", "key_version_list", "key_version_schedule_deletion", "key_version_cancel_deletion",
		"encrypt", "decrypt", "generate_data_encryption_key", "sign", "verify", "export_key",
		"secret_create",
	}
	for _, a := range keyScoped {
		reg(a, "key_ocid", keysEP, credsCompVault)
	}

	// key_version_ocid → the two-hop key-versions picker (compartment + vault + key).
	for _, a := range []string{"key_version_get", "key_version_schedule_deletion", "key_version_cancel_deletion"} {
		reg(a, "key_version_ocid", keyVersionsEP, credsCompVaultKey)
	}

	// secret_ocid → the secrets picker (compartment-scoped).
	for _, a := range []string{
		"secret_get", "secret_update", "secret_schedule_deletion", "secret_cancel_deletion", "secret_change_compartment",
		"secret_version_get", "secret_version_list", "secret_version_schedule_deletion", "secret_version_cancel_deletion",
	} {
		reg(a, "secret_ocid", secretsEP, credsComp)
	}
	// The two bundle actions also carry a vault_ocid scope input — forward it (credsCompVault)
	// so the secrets picker actually narrows to the chosen vault instead of the whole compartment.
	for _, a := range []string{"secret_bundle_get", "secret_bundle_versions_list"} {
		reg(a, "secret_ocid", secretsEP, credsCompVault)
	}

	// change-compartment destination pickers.
	for _, a := range []string{"vault_change_compartment", "key_change_compartment", "secret_change_compartment"} {
		reg(a, "destination_compartment_ocid", comp, creds)
	}
}

func (s *Service) oracleVaultClient(c *gin.Context) (keymanagement.KmsVaultClient, bool) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return keymanagement.KmsVaultClient{}, false
	}
	client, err := keymanagement.NewKmsVaultClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return keymanagement.KmsVaultClient{}, false
	}
	client.HTTPClient = ociOptionsHTTPClient
	return client, true
}

// oracleVaultManagementClient resolves the selected vault's management endpoint and returns
// a KmsManagementClient pointed at it — the two-hop the keys / key-versions pickers need.
func (s *Service) oracleVaultManagementClient(c *gin.Context) (keymanagement.KmsManagementClient, bool) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return keymanagement.KmsManagementClient{}, false
	}
	vaultID, ok := s.ociRequireDependency(c, "vault_ocid", "Select a vault first")
	if !ok {
		return keymanagement.KmsManagementClient{}, false
	}
	vc, err := keymanagement.NewKmsVaultClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return keymanagement.KmsManagementClient{}, false
	}
	vc.HTTPClient = ociOptionsHTTPClient
	vResp, err := vc.GetVault(c.Request.Context(), keymanagement.GetVaultRequest{VaultId: &vaultID})
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return keymanagement.KmsManagementClient{}, false
	}
	endpoint := ""
	if vResp.Vault.ManagementEndpoint != nil {
		endpoint = *vResp.Vault.ManagementEndpoint
	}
	mc, err := keymanagement.NewKmsManagementClientWithConfigurationProvider(provider, endpoint)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return keymanagement.KmsManagementClient{}, false
	}
	mc.HTTPClient = ociOptionsHTTPClient
	return mc, true
}

func (s *Service) getOracleVaultVaults(c *gin.Context) {
	client, ok := s.oracleVaultClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := keymanagement.ListVaultsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListVaults(c.Request.Context(), req)
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

func (s *Service) getOracleVaultKeys(c *gin.Context) {
	client, ok := s.oracleVaultManagementClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := keymanagement.ListKeysRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListKeys(c.Request.Context(), req)
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

func (s *Service) getOracleVaultKeyVersions(c *gin.Context) {
	client, ok := s.oracleVaultManagementClient(c)
	if !ok {
		return
	}
	keyID, ok := s.ociRequireDependency(c, "key_ocid", "Select a key first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := keymanagement.ListKeyVersionsRequest{KeyId: &keyID}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListKeyVersions(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			id := strDeref(resp.Items[i].Id)
			name := id
			if resp.Items[i].TimeCreated != nil {
				name = resp.Items[i].TimeCreated.Time.UTC().Format("2006-01-02 15:04") + " · " + id
			}
			opts = append(opts, api.InputOption{Name: name, Value: id})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleVaultSecrets(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := ovault.NewVaultsClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := ovault.ListSecretsRequest{CompartmentId: &compartment}
	if vaultID := strings.TrimSpace(c.Query("vault_ocid")); vaultID != "" && !strings.HasPrefix(vaultID, "${") {
		req.VaultId = &vaultID
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListSecrets(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].SecretName), Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}
