package http

import (
	gohttp "net/http"

	"github.com/gin-gonic/gin"
	eventssdk "github.com/oracle/oci-go-sdk/v65/events"
	functionssdk "github.com/oracle/oci-go-sdk/v65/functions"
	monitoringsdk "github.com/oracle/oci-go-sdk/v65/monitoring"
	queuesdk "github.com/oracle/oci-go-sdk/v65/queue"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud Queue, Functions, Monitoring and Events nodes.
// Same shape as the sibling OCI proxies: build an OCI ConfigurationProvider from the node's
// connection (private key resolved server-side from ${secrets.X}), call the list APIs, and return
// {options:[{name,value}]} — or an HTTP 200 + {"error":...} fallback. Pickers filter to ACTIVE.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	rulesEP := "/api/v1/action/options/oracle-events-rules"
	queuesEP := "/api/v1/action/options/oracle-queue-queues"
	appsEP := "/api/v1/action/options/oracle-functions-applications"
	alarmsEP := "/api/v1/action/options/oracle-monitoring-alarms"

	reg := func(node, id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/"+node+"/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	allByNode := map[string][]string{
		"events": {"rule_create", "rule_get", "rule_list", "rule_update", "rule_delete", "rule_change_compartment"},
		"queue": {"queue_create", "queue_get", "queue_list", "queue_update", "queue_delete", "queue_change_compartment", "queue_purge",
			"work_request_get", "work_request_list", "work_request_errors_list", "work_request_logs_list",
			"message_put", "message_get", "message_delete", "message_delete_batch", "message_update", "message_update_batch", "get_stats", "list_channels"},
		"functions": {"application_create", "application_get", "application_list", "application_update", "application_delete", "application_change_compartment",
			"function_create", "function_get", "function_list", "function_update", "function_delete", "function_invoke", "pbf_listing_list", "pbf_listing_get", "triggers_list"},
		"monitoring": {"alarm_create", "post_metric_data", "metrics_list", "summarize_metrics_data", "alarm_get", "alarm_list", "alarm_update", "alarm_delete",
			"alarm_change_compartment", "alarm_history_get", "alarms_status_list", "alarm_suppression_create", "alarm_suppression_get", "alarm_suppression_list", "alarm_suppression_delete"},
	}
	for node, ids := range allByNode {
		for _, id := range ids {
			reg(node, id, "compartment_ocid", comp, creds)
		}
	}

	// Events: rule picker + destination compartment.
	for _, id := range []string{"rule_get", "rule_update", "rule_delete", "rule_change_compartment"} {
		reg("events", id, "rule_ocid", rulesEP, credsComp)
	}
	reg("events", "rule_change_compartment", "destination_compartment_ocid", comp, creds)

	// Queue: queue picker on every action that targets a queue by OCID + destination compartment.
	for _, id := range []string{"queue_get", "queue_update", "queue_delete", "queue_change_compartment", "queue_purge",
		"message_put", "message_get", "message_delete", "message_delete_batch", "message_update", "message_update_batch", "get_stats", "list_channels"} {
		reg("queue", id, "queue_ocid", queuesEP, credsComp)
	}
	reg("queue", "queue_change_compartment", "destination_compartment_ocid", comp, creds)

	// Functions: applications picker on the app ops + the two actions that take an application_id;
	// no function_ocid picker (ListFunctions needs an app, so function OCIDs are entered/chained).
	for _, id := range []string{"application_get", "application_update", "application_delete", "application_change_compartment"} {
		reg("functions", id, "application_ocid", appsEP, credsComp)
	}
	reg("functions", "function_create", "application_id", appsEP, credsComp)
	reg("functions", "function_list", "application_id", appsEP, credsComp)
	reg("functions", "application_change_compartment", "destination_compartment_ocid", comp, creds)

	// Monitoring: alarm picker on alarm-targeting actions + destination compartment.
	for _, id := range []string{"alarm_get", "alarm_update", "alarm_delete", "alarm_change_compartment", "alarm_history_get", "alarm_suppression_create"} {
		reg("monitoring", id, "alarm_ocid", alarmsEP, credsComp)
	}
	reg("monitoring", "alarm_change_compartment", "destination_compartment_ocid", comp, creds)
}

// ---- Events ----
func (s *Service) getOracleEventsRules(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := eventssdk.NewEventsClientWithConfigurationProvider(provider)
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
	req := eventssdk.ListRulesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListRules(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != eventssdk.RuleLifecycleStateActive {
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

// ---- Queue ----
func (s *Service) getOracleQueueQueues(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := queuesdk.NewQueueAdminClientWithConfigurationProvider(provider)
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
	req := queuesdk.ListQueuesRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListQueues(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != queuesdk.QueueLifecycleStateActive {
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

// ---- Functions ----
func (s *Service) getOracleFunctionsApplications(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := functionssdk.NewFunctionsManagementClientWithConfigurationProvider(provider)
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
	req := functionssdk.ListApplicationsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListApplications(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != functionssdk.ApplicationLifecycleStateActive {
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

// ---- Monitoring ----
func (s *Service) getOracleMonitoringAlarms(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	client, err := monitoringsdk.NewMonitoringClientWithConfigurationProvider(provider)
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
	req := monitoringsdk.ListAlarmsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListAlarms(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != monitoringsdk.AlarmLifecycleStateActive {
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
