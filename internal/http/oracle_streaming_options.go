package http

import (
	gohttp "net/http"

	"github.com/gin-gonic/gin"
	streamingsdk "github.com/oracle/oci-go-sdk/v65/streaming"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud Streaming node. Same shape as the sibling
// OCI proxies: build an OCI ConfigurationProvider from the node's connection (private key
// resolved server-side from ${secrets.X}), call the StreamAdmin list APIs, and return
// {options:[{name,value}]} — or an HTTP 200 + {"error":...} fallback so the editor shows the
// message inline and falls back to manual entry. Pickers filter to ACTIVE.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	streamsEP := "/api/v1/action/options/oracle-streaming-streams"
	poolsEP := "/api/v1/action/options/oracle-streaming-stream-pools"
	harnessEP := "/api/v1/action/options/oracle-streaming-connect-harnesses"

	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/streaming/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	allActions := []string{
		"stream_create", "stream_get", "stream_list", "stream_update", "stream_delete", "stream_change_compartment",
		"stream_pool_create", "stream_pool_get", "stream_pool_list", "stream_pool_update", "stream_pool_delete", "stream_pool_change_compartment",
		"connect_harness_create", "connect_harness_get", "connect_harness_list", "connect_harness_update", "connect_harness_delete", "connect_harness_change_compartment",
		"work_request_get", "work_request_list", "work_request_errors_list", "work_request_logs_list",
		"message_put", "message_get", "cursor_create", "group_cursor_create", "group_get", "group_update", "consumer_commit", "consumer_heartbeat",
	}
	for _, a := range allActions {
		reg(a, "compartment_ocid", comp, creds)
	}

	// stream_ocid → the streams picker (compartment-scoped). Every action that targets an
	// existing stream by OCID, including the whole data plane.
	for _, a := range []string{
		"stream_get", "stream_update", "stream_delete", "stream_change_compartment",
		"message_put", "message_get", "cursor_create", "group_cursor_create", "group_get", "group_update", "consumer_commit", "consumer_heartbeat",
	} {
		reg(a, "stream_ocid", streamsEP, credsComp)
	}

	// stream_pool_ocid → the stream-pools picker; stream_pool_id is the optional pool selector
	// on stream create/list, pointed at the same picker.
	for _, a := range []string{"stream_pool_get", "stream_pool_update", "stream_pool_delete", "stream_pool_change_compartment"} {
		reg(a, "stream_pool_ocid", poolsEP, credsComp)
	}
	reg("stream_create", "stream_pool_id", poolsEP, credsComp)
	reg("stream_list", "stream_pool_id", poolsEP, credsComp)
	reg("stream_update", "stream_pool_id", poolsEP, credsComp) // "Move to Stream Pool" — same picker as create/list

	// connect_harness_ocid → the connect-harnesses picker.
	for _, a := range []string{"connect_harness_get", "connect_harness_update", "connect_harness_delete", "connect_harness_change_compartment"} {
		reg(a, "connect_harness_ocid", harnessEP, credsComp)
	}

	// change-compartment destination pickers.
	for _, a := range []string{"stream_change_compartment", "stream_pool_change_compartment", "connect_harness_change_compartment"} {
		reg(a, "destination_compartment_ocid", comp, creds)
	}
}

func (s *Service) oracleStreamingClient(c *gin.Context) (streamingsdk.StreamAdminClient, bool) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return streamingsdk.StreamAdminClient{}, false
	}
	client, err := streamingsdk.NewStreamAdminClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return streamingsdk.StreamAdminClient{}, false
	}
	client.HTTPClient = ociOptionsHTTPClient
	return client, true
}

func (s *Service) getOracleStreamingStreams(c *gin.Context) {
	client, ok := s.oracleStreamingClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := streamingsdk.ListStreamsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListStreams(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != streamingsdk.StreamSummaryLifecycleStateActive {
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

func (s *Service) getOracleStreamingStreamPools(c *gin.Context) {
	client, ok := s.oracleStreamingClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := streamingsdk.ListStreamPoolsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListStreamPools(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != streamingsdk.StreamPoolSummaryLifecycleStateActive {
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

func (s *Service) getOracleStreamingConnectHarnesses(c *gin.Context) {
	client, ok := s.oracleStreamingClient(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	opts := []api.InputOption{}
	req := streamingsdk.ListConnectHarnessesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListConnectHarnesses(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != streamingsdk.ConnectHarnessSummaryLifecycleStateActive {
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
