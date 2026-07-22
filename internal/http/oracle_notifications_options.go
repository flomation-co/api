package http

import (
	gohttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	onssdk "github.com/oracle/oci-go-sdk/v65/ons"

	api "flomation.app/automate/api"
)

// Live dropdown option proxies for the Oracle Cloud Notifications (ONS) node. Same shape as
// the sibling OCI proxies: build an OCI ConfigurationProvider from the node's connection
// (private key resolved server-side from ${secrets.X}), call the ONS list APIs, and return
// {options:[{name,value}]} — or an HTTP 200 + {"error":...} fallback. Pickers filter by
// lifecycle state so deleted/creating resources don't pollute the dropdowns.

func init() {
	creds := []string{"tenancy_ocid", "user_ocid", "region", "fingerprint", "private_key", "private_key_passphrase"}
	credsComp := append(append([]string{}, creds...), "compartment_ocid")

	comp := "/api/v1/action/options/oracle-compartments"
	topicsEP := "/api/v1/action/options/oracle-notifications-topics"
	subsEP := "/api/v1/action/options/oracle-notifications-subscriptions"

	reg := func(id, input, endpoint string, params []string) {
		dynamicOptionsMetadata["oracle/notifications/"+id+"#"+input] = api.InputDynamicOptions{Endpoint: endpoint, Params: params}
	}

	// compartment_ocid picker on every action.
	allActions := []string{
		"topic_create", "topic_get", "topic_list", "topic_update", "topic_delete", "topic_change_compartment",
		"subscription_create", "subscription_get", "subscription_list", "subscription_update", "subscription_delete",
		"subscription_confirm", "subscription_unsubscribe", "subscription_resend_confirmation", "subscription_change_compartment",
		"publish_message",
	}
	for _, a := range allActions {
		reg(a, "compartment_ocid", comp, creds)
	}

	// topic_ocid → the topics picker (compartment-scoped): topic ops + the actions that target a topic.
	for _, a := range []string{
		"topic_get", "topic_update", "topic_delete", "topic_change_compartment",
		"subscription_create", "subscription_list", "publish_message",
	} {
		reg(a, "topic_ocid", topicsEP, credsComp)
	}

	// subscription_ocid → the subscriptions picker (compartment-scoped).
	for _, a := range []string{
		"subscription_get", "subscription_update", "subscription_delete",
		"subscription_confirm", "subscription_unsubscribe", "subscription_resend_confirmation", "subscription_change_compartment",
	} {
		reg(a, "subscription_ocid", subsEP, credsComp)
	}

	// change-compartment destination pickers.
	for _, a := range []string{"topic_change_compartment", "subscription_change_compartment"} {
		reg(a, "destination_compartment_ocid", comp, creds)
	}
}

func (s *Service) getOracleNotificationsTopics(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := onssdk.NewNotificationControlPlaneClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := onssdk.ListTopicsRequest{CompartmentId: &compartment}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListTopics(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			if resp.Items[i].LifecycleState != onssdk.NotificationTopicSummaryLifecycleStateActive {
				continue
			}
			opts = append(opts, api.InputOption{Name: strDeref(resp.Items[i].Name), Value: strDeref(resp.Items[i].TopicId)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}

func (s *Service) getOracleNotificationsSubscriptions(c *gin.Context) {
	provider, ok := s.buildOCIProvider(c)
	if !ok {
		return
	}
	compartment, ok := s.ociRequireDependency(c, "compartment_ocid", "Select a compartment first")
	if !ok {
		return
	}
	client, err := onssdk.NewNotificationDataPlaneClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
		return
	}
	client.HTTPClient = ociOptionsHTTPClient
	opts := []api.InputOption{}
	req := onssdk.ListSubscriptionsRequest{CompartmentId: &compartment}
	if topicID := strings.TrimSpace(c.Query("topic_ocid")); topicID != "" && !strings.HasPrefix(topicID, "${") {
		req.TopicId = &topicID
	}
	for page := 0; page < ociOptionsMaxPages; page++ {
		resp, err := client.ListSubscriptions(c.Request.Context(), req)
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": ociOptErr(err)})
			return
		}
		for i := range resp.Items {
			// Active and pending subscriptions are valid targets (pending ones can be confirmed
			// or have their confirmation resent); drop only deleted ones.
			if resp.Items[i].LifecycleState == onssdk.SubscriptionSummaryLifecycleStateDeleted {
				continue
			}
			label := strDeref(resp.Items[i].Protocol) + " · " + strDeref(resp.Items[i].Endpoint)
			opts = append(opts, api.InputOption{Name: label, Value: strDeref(resp.Items[i].Id)})
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			break
		}
		req.Page = resp.OpcNextPage
	}
	c.JSON(gohttp.StatusOK, gin.H{"options": opts})
}
