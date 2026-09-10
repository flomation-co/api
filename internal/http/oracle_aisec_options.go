package http

import (
	gohttp "net/http"

	"github.com/gin-gonic/gin"
	aidocumentsdk "github.com/oracle/oci-go-sdk/v65/aidocument"
	ailanguagesdk "github.com/oracle/oci-go-sdk/v65/ailanguage"
	aispeechsdk "github.com/oracle/oci-go-sdk/v65/aispeech"
	aivisionsdk "github.com/oracle/oci-go-sdk/v65/aivision"
	bastionsdk "github.com/oracle/oci-go-sdk/v65/bastion"
	cloudguardsdk "github.com/oracle/oci-go-sdk/v65/cloudguard"
	genaisdk "github.com/oracle/oci-go-sdk/v65/generativeai"
	vsssdk "github.com/oracle/oci-go-sdk/v65/vulnerabilityscanning"
	waasdk "github.com/oracle/oci-go-sdk/v65/waa"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the 9 AI/security Oracle Cloud nodes. Each node gets its
// compartment picker on every action, a primary-resource picker (compartment-scoped), and a
// destination-compartment picker on its change-compartment actions. Same shape as the sibling OCI
// proxies; 200 + {"error"} fallback.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")
	comp := "/api/v1/action/options/oracle-compartments"
	reg := func(node, id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/"+node+"/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}
	byNode := map[string][]string{
		"generativeai":          {"chat", "embed_text", "generate_text", "rerank_text", "summarize_text", "model_list", "model_get", "endpoint_create", "endpoint_get", "endpoint_list", "endpoint_update", "endpoint_delete", "endpoint_change_compartment", "dedicated_ai_cluster_list"},
		"language":              {"batch_detect_dominant_language", "batch_detect_language_sentiments", "batch_detect_language_entities", "batch_detect_language_key_phrases", "batch_detect_language_pii_entities", "batch_detect_language_text_classification", "batch_language_translation", "project_create", "project_get", "project_list", "project_delete", "project_change_compartment", "model_get", "model_list", "endpoint_get", "endpoint_list"},
		"vision":                {"analyze_image", "analyze_document", "project_create", "project_get", "project_list", "project_update", "project_delete", "project_change_compartment", "model_get", "model_list"},
		"documentunderstanding": {"analyze_document", "processor_job_create", "processor_job_get", "processor_job_cancel", "project_create", "project_get", "project_list", "project_update", "project_delete", "project_change_compartment", "model_get", "model_list"},
		"speech":                {"synthesize_speech", "list_voices", "transcription_job_create", "transcription_job_get", "transcription_job_list", "transcription_job_update", "transcription_job_cancel", "transcription_job_change_compartment", "customization_create", "customization_get", "customization_list", "customization_update", "customization_delete"},
		"bastion":               {"bastion_create", "bastion_get", "bastion_list", "bastion_update", "bastion_delete", "bastion_change_compartment", "session_create", "session_get", "session_list", "session_delete"},
		"waa":                   {"web_app_acceleration_create", "web_app_acceleration_get", "web_app_acceleration_list", "web_app_acceleration_update", "web_app_acceleration_delete", "web_app_acceleration_change_compartment", "web_app_acceleration_purge_cache", "policy_create", "policy_get", "policy_list", "policy_update", "policy_delete", "policy_change_compartment"},
		"vulnerabilityscanning": {"host_scan_recipe_create", "host_scan_recipe_get", "host_scan_recipe_list", "host_scan_recipe_delete", "host_scan_recipe_change_compartment", "host_scan_target_create", "host_scan_target_get", "host_scan_target_list", "host_scan_target_delete", "host_scan_target_change_compartment", "container_scan_recipe_create", "container_scan_recipe_get", "container_scan_recipe_list", "container_scan_recipe_delete", "container_scan_target_create", "container_scan_target_get", "container_scan_target_list", "container_scan_target_delete", "host_agent_scan_result_list"},
		"cloudguard":            {"detector_recipe_create", "detector_recipe_get", "detector_recipe_list", "detector_recipe_update", "detector_recipe_delete", "detector_recipe_change_compartment", "target_create", "target_get", "target_list", "target_update", "target_delete", "target_change_compartment", "problem_get", "problem_list", "problem_update_status", "managed_list_create", "managed_list_get", "managed_list_list", "managed_list_delete", "responder_recipe_list"},
	}
	for node, ids := range byNode {
		for _, id := range ids {
			reg(node, id, "compartment_ocid", comp, creds)
		}
	}

	gaiModels := "/api/v1/action/options/oracle-generativeai-models"
	for _, id := range []string{"chat", "embed_text", "generate_text", "rerank_text", "summarize_text"} {
		reg("generativeai", id, "model_id", gaiModels, credsComp)
	}
	reg("generativeai", "model_get", "model_ocid", gaiModels, credsComp)
	reg("generativeai", "endpoint_change_compartment", "destination_compartment_ocid", comp, creds)

	langProj := "/api/v1/action/options/oracle-language-projects"
	for _, id := range []string{"project_get", "project_delete", "project_change_compartment"} {
		reg("language", id, "project_ocid", langProj, credsComp)
	}
	reg("language", "project_change_compartment", "destination_compartment_ocid", comp, creds)

	visProj := "/api/v1/action/options/oracle-vision-projects"
	for _, id := range []string{"project_get", "project_update", "project_delete", "project_change_compartment"} {
		reg("vision", id, "project_ocid", visProj, credsComp)
	}
	reg("vision", "project_change_compartment", "destination_compartment_ocid", comp, creds)

	duProj := "/api/v1/action/options/oracle-documentunderstanding-projects"
	for _, id := range []string{"project_get", "project_update", "project_delete", "project_change_compartment"} {
		reg("documentunderstanding", id, "project_ocid", duProj, credsComp)
	}
	reg("documentunderstanding", "project_change_compartment", "destination_compartment_ocid", comp, creds)

	spJobs := "/api/v1/action/options/oracle-speech-transcription-jobs"
	for _, id := range []string{"transcription_job_get", "transcription_job_update", "transcription_job_cancel", "transcription_job_change_compartment"} {
		reg("speech", id, "transcription_job_ocid", spJobs, credsComp)
	}
	reg("speech", "transcription_job_change_compartment", "destination_compartment_ocid", comp, creds)

	bastions := "/api/v1/action/options/oracle-bastions"
	for _, id := range []string{"bastion_get", "bastion_update", "bastion_delete", "bastion_change_compartment", "session_create", "session_list"} {
		reg("bastion", id, "bastion_ocid", bastions, credsComp)
	}
	reg("bastion", "bastion_change_compartment", "destination_compartment_ocid", comp, creds)

	waaAcc := "/api/v1/action/options/oracle-waa-accelerations"
	for _, id := range []string{"web_app_acceleration_get", "web_app_acceleration_update", "web_app_acceleration_delete", "web_app_acceleration_change_compartment", "web_app_acceleration_purge_cache"} {
		reg("waa", id, "web_app_acceleration_ocid", waaAcc, credsComp)
	}
	for _, id := range []string{"web_app_acceleration_change_compartment", "policy_change_compartment"} {
		reg("waa", id, "destination_compartment_ocid", comp, creds)
	}

	vssRec := "/api/v1/action/options/oracle-vss-host-scan-recipes"
	for _, id := range []string{"host_scan_recipe_get", "host_scan_recipe_delete", "host_scan_recipe_change_compartment", "host_scan_target_create"} {
		reg("vulnerabilityscanning", id, "host_scan_recipe_ocid", vssRec, credsComp)
	}
	for _, id := range []string{"host_scan_recipe_change_compartment", "host_scan_target_change_compartment"} {
		reg("vulnerabilityscanning", id, "destination_compartment_ocid", comp, creds)
	}

	cgRec := "/api/v1/action/options/oracle-cloudguard-detector-recipes"
	for _, id := range []string{"detector_recipe_get", "detector_recipe_update", "detector_recipe_delete", "detector_recipe_change_compartment"} {
		reg("cloudguard", id, "detector_recipe_ocid", cgRec, credsComp)
	}
	for _, id := range []string{"detector_recipe_change_compartment", "target_change_compartment"} {
		reg("cloudguard", id, "destination_compartment_ocid", comp, creds)
	}
}

func (s *Service) getOracleGenerativeAiModels(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := genaisdk.NewGenerativeAiClientWithConfigurationProvider(provider)
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
	req := genaisdk.ListModelsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListModels(c.Request.Context(), req)
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

func (s *Service) getOracleLanguageProjects(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := ailanguagesdk.NewAIServiceLanguageClientWithConfigurationProvider(provider)
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
	req := ailanguagesdk.ListProjectsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListProjects(c.Request.Context(), req)
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

func (s *Service) getOracleVisionProjects(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := aivisionsdk.NewAIServiceVisionClientWithConfigurationProvider(provider)
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
	req := aivisionsdk.ListProjectsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListProjects(c.Request.Context(), req)
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

func (s *Service) getOracleDocumentUnderstandingProjects(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := aidocumentsdk.NewAIServiceDocumentClientWithConfigurationProvider(provider)
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
	req := aidocumentsdk.ListProjectsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListProjects(c.Request.Context(), req)
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

func (s *Service) getOracleSpeechTranscriptionJobs(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := aispeechsdk.NewAIServiceSpeechClientWithConfigurationProvider(provider)
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
	req := aispeechsdk.ListTranscriptionJobsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListTranscriptionJobs(c.Request.Context(), req)
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

func (s *Service) getOracleBastions(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := bastionsdk.NewBastionClientWithConfigurationProvider(provider)
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
	req := bastionsdk.ListBastionsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListBastions(c.Request.Context(), req)
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

func (s *Service) getOracleWaaAccelerations(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := waasdk.NewWaaClientWithConfigurationProvider(provider)
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
	req := waasdk.ListWebAppAccelerationsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListWebAppAccelerations(c.Request.Context(), req)
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

func (s *Service) getOracleVssHostScanRecipes(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := vsssdk.NewVulnerabilityScanningClientWithConfigurationProvider(provider)
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
	req := vsssdk.ListHostScanRecipesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListHostScanRecipes(c.Request.Context(), req)
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

func (s *Service) getOracleCloudGuardDetectorRecipes(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := cloudguardsdk.NewCloudGuardClientWithConfigurationProvider(provider)
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
	req := cloudguardsdk.ListDetectorRecipesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListDetectorRecipes(c.Request.Context(), req)
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
