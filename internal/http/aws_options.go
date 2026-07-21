package http

import (
	"context"
	"fmt"
	gohttp "net/http"
	"sort"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/gin-gonic/gin"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
)

// Live dropdowns for the AWS actions (aws/rds/*, aws/ec2/*). Rather than a wall
// of near-identical "<action>#<input>" markers, these are injected by RULE in
// GetActions: any action whose ID starts with "aws/" and whose input name is one
// of awsResourceInputs gets the matching picker (see awsDynamicOption, wired at
// the injection site in action.go). That keeps ~100 AWS actions covered with no
// per-action bookkeeping.
//
// The editor forwards the node's credential block (region + access keys or a
// managed credential) as query params; the plaintext keys arrive as ${secrets.X}
// references and are resolved server-side, gated by rbac.EnvironmentView — the
// same security invariant as the other option proxies (a resolved secret
// authenticates a request to AWS, so an environment-denied member must not be
// able to trigger it). Managed (${credentials.X}) auth can't be resolved here, so
// those return an inline message and fall back to manual entry — the flow itself
// still runs fine with the managed credential.
//
// Errors follow the option-proxy convention: HTTP 200 + {"error": …} so the
// editor renders the message under the field and keeps manual entry available.

// awsOptionParams are the node inputs forwarded on every AWS option fetch.
var awsOptionParams = []string{
	"aws_region", "aws_access_key", "aws_secret_key", "aws_session_token",
	"assume_role_arn", "external_id", "credential", "environment",
	// engine is only consumed by the instance-class picker (DescribeOrderable
	// requires it); the resource pickers ignore it.
	"engine",
}

// awsResourceInputs maps an AWS action input name to the picker slug that fills
// it. Used both for the rule-based marker injection and to register routes.
var awsResourceInputs = map[string]string{
	"vpc_security_group_ids": "security-groups",
	"subnet_ids":             "subnets",
	"db_subnet_group_name":   "subnet-groups",
	"kms_key_id":             "kms-keys",
	"role_arn":               "iam-roles",
	"sns_topic_arn":          "sns-topics",
	"db_instance_class":      "db-instance-classes",
	// VPC networking resources (aws/vpc/* actions).
	"vpc_id":                    "vpcs",
	"subnet_id":                 "subnets",
	"route_table_id":            "route-tables",
	"internet_gateway_id":       "internet-gateways",
	"nat_gateway_id":            "nat-gateways",
	"network_acl_id":            "network-acls",
	"vpc_endpoint_id":           "vpc-endpoints",
	"vpc_peering_connection_id": "vpc-peering-connections",
	"transit_gateway_id":        "transit-gateways",
	"allocation_id":             "elastic-ips",
	"dhcp_options_id":           "dhcp-options",
	"network_interface_id":      "network-interfaces",
	"customer_gateway_id":       "customer-gateways",
	"vpn_gateway_id":            "vpn-gateways",
	// VPC v2 (transit-gateway wiring + endpoint services).
	"transit_gateway_attachment_id":  "transit-gateway-attachments",
	"transit_gateway_route_table_id": "transit-gateway-route-tables",
	"service_id":                     "vpc-endpoint-services",
}

// awsDynamicOption returns the dynamic-options marker for an AWS action input, or
// (_, false) when the input isn't an AWS-resource picker. Called from the
// GetActions injection loop as a fallback after the exact dynamicOptionsMetadata
// map misses.
func awsDynamicOption(actionID, inputName string) (api.InputDynamicOptions, bool) {
	if !strings.HasPrefix(actionID, "aws/") {
		return api.InputDynamicOptions{}, false
	}
	slug, ok := awsResourceInputs[inputName]
	if !ok {
		return api.InputDynamicOptions{}, false
	}
	return api.InputDynamicOptions{
		Endpoint: "/api/v1/action/options/aws-" + slug,
		Params:   awsOptionParams,
	}, true
}

type awsOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// resolveAWSConfig builds an aws.Config from the forwarded credential params.
// Only the static-keys path is supported for listing (the common case); managed
// credentials and assume-role return an inline message rather than an error, so
// the picker degrades to manual entry. ok==false with an empty message means
// checkPermission already wrote the response.
func (s *Service) resolveAWSConfig(c *gin.Context) (awssdk.Config, string, bool) {
	region := strings.TrimSpace(c.Query("aws_region"))
	if region == "" {
		return awssdk.Config{}, "Choose a region to load this list", false
	}

	accessRaw := strings.TrimSpace(c.Query("aws_access_key"))
	secretRaw := strings.TrimSpace(c.Query("aws_secret_key"))
	credentialRaw := strings.TrimSpace(c.Query("credential"))

	if accessRaw == "" || secretRaw == "" {
		if credentialRaw != "" || strings.TrimSpace(c.Query("assume_role_arn")) != "" {
			return awssdk.Config{}, "Managed-credential and assume-role auth can't load this list — pick with access keys, or type the value in (the flow still runs)", false
		}
		return awssdk.Config{}, "Enter AWS access keys to load this list", false
	}

	access, msg, ok := s.resolveAWSSecretParam(c, accessRaw, "AWS access key")
	if !ok {
		return awssdk.Config{}, msg, false
	}
	secret, msg, ok := s.resolveAWSSecretParam(c, secretRaw, "AWS secret key")
	if !ok {
		return awssdk.Config{}, msg, false
	}
	session := ""
	if raw := strings.TrimSpace(c.Query("aws_session_token")); raw != "" {
		session, msg, ok = s.resolveAWSSecretParam(c, raw, "AWS session token")
		if !ok {
			return awssdk.Config{}, msg, false
		}
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: awssdk.Credentials{AccessKeyID: access, SecretAccessKey: secret, SessionToken: session, Source: "Flomation option proxy"},
		}),
	)
	if err != nil {
		return awssdk.Config{}, "Could not build AWS client: " + err.Error(), false
	}
	return cfg, "", true
}

// resolveAWSSecretParam resolves a possibly-${secrets.X} credential param,
// mirroring resolveAWXSecret: managed credentials are rejected, ${...} refs are
// resolved from the environment behind the EnvironmentView gate, and a literal
// value passes through.
func (s *Service) resolveAWSSecretParam(c *gin.Context, raw, label string) (string, string, bool) {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, "${credentials.") || strings.HasPrefix(value, "${credential.") {
		return "", "Managed credentials can't load this list — use an environment secret for the " + label, false
	}
	if strings.HasPrefix(value, "${") {
		environmentID := strings.TrimSpace(c.Query("environment"))
		if environmentID == "" {
			return "", "Select an environment to resolve the " + label + " secret", false
		}
		if !s.checkPermission(c, rbac.EnvironmentView) {
			return "", "", false // checkPermission wrote the response
		}
		resolved, errMsg := s.resolveEnvironmentSecret(c, environmentID, value)
		if errMsg != "" {
			return "", errMsg, false
		}
		return resolved, "", true
	}
	return value, "", true
}

// awsOptions returns the handler for one AWS picker slug.
func (s *Service) awsOptions(slug string) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, msg, ok := s.resolveAWSConfig(c)
		if !ok {
			if msg != "" {
				c.JSON(gohttp.StatusOK, gin.H{"error": msg})
			}
			return
		}

		var (
			opts []awsOption
			err  error
		)
		ctx := context.Background()
		switch slug {
		case "security-groups":
			opts, err = listSecurityGroups(ctx, cfg)
		case "subnets":
			opts, err = listSubnets(ctx, cfg)
		case "subnet-groups":
			opts, err = listDBSubnetGroups(ctx, cfg)
		case "kms-keys":
			opts, err = listKMSKeys(ctx, cfg)
		case "iam-roles":
			opts, err = listIAMRoles(ctx, cfg)
		case "sns-topics":
			opts, err = listSNSTopics(ctx, cfg)
		case "db-instance-classes":
			engine := strings.TrimSpace(c.Query("engine"))
			if engine == "" {
				c.JSON(gohttp.StatusOK, gin.H{"error": "Choose an engine to list the instance classes available in this region"})
				return
			}
			opts, err = listOrderableInstanceClasses(ctx, cfg, engine)
		case "vpcs":
			opts, err = listVPCs(ctx, cfg)
		case "route-tables":
			opts, err = listRouteTables(ctx, cfg)
		case "internet-gateways":
			opts, err = listInternetGateways(ctx, cfg)
		case "nat-gateways":
			opts, err = listNatGateways(ctx, cfg)
		case "network-acls":
			opts, err = listNetworkAcls(ctx, cfg)
		case "vpc-endpoints":
			opts, err = listVpcEndpoints(ctx, cfg)
		case "vpc-peering-connections":
			opts, err = listVpcPeeringConnections(ctx, cfg)
		case "transit-gateways":
			opts, err = listTransitGateways(ctx, cfg)
		case "elastic-ips":
			opts, err = listElasticIPs(ctx, cfg)
		case "dhcp-options":
			opts, err = listDhcpOptions(ctx, cfg)
		case "network-interfaces":
			opts, err = listNetworkInterfaces(ctx, cfg)
		case "customer-gateways":
			opts, err = listCustomerGateways(ctx, cfg)
		case "vpn-gateways":
			opts, err = listVpnGateways(ctx, cfg)
		case "transit-gateway-attachments":
			opts, err = listTransitGatewayAttachments(ctx, cfg)
		case "transit-gateway-route-tables":
			opts, err = listTransitGatewayRouteTables(ctx, cfg)
		case "vpc-endpoint-services":
			opts, err = listVpcEndpointServices(ctx, cfg)
		default:
			c.JSON(gohttp.StatusOK, gin.H{"error": "unknown AWS resource list"})
			return
		}
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": err.Error()})
			return
		}
		sort.Slice(opts, func(i, j int) bool { return strings.ToLower(opts[i].Name) < strings.ToLower(opts[j].Name) })
		c.JSON(gohttp.StatusOK, gin.H{"options": opts})
	}
}

func listSecurityGroups(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeSecurityGroupsPaginator(client, &ec2.DescribeSecurityGroupsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range page.SecurityGroups {
			id := awssdk.ToString(g.GroupId)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s (%s)", awssdk.ToString(g.GroupName), id), Value: id})
		}
	}
	return opts, nil
}

func listSubnets(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeSubnetsPaginator(client, &ec2.DescribeSubnetsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, sub := range page.Subnets {
			id := awssdk.ToString(sub.SubnetId)
			name := id
			for _, t := range sub.Tags {
				if awssdk.ToString(t.Key) == "Name" {
					name = fmt.Sprintf("%s (%s)", awssdk.ToString(t.Value), id)
				}
			}
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s, %s", name, awssdk.ToString(sub.AvailabilityZone), awssdk.ToString(sub.CidrBlock)), Value: id})
		}
	}
	return opts, nil
}

func listDBSubnetGroups(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := rds.NewFromConfig(cfg)
	var opts []awsOption
	p := rds.NewDescribeDBSubnetGroupsPaginator(client, &rds.DescribeDBSubnetGroupsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range page.DBSubnetGroups {
			name := awssdk.ToString(g.DBSubnetGroupName)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s (%s)", name, awssdk.ToString(g.VpcId)), Value: name})
		}
	}
	return opts, nil
}

func listKMSKeys(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := kms.NewFromConfig(cfg)
	var opts []awsOption
	p := kms.NewListAliasesPaginator(client, &kms.ListAliasesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range page.Aliases {
			target := awssdk.ToString(a.TargetKeyId)
			if target == "" {
				continue // AWS-reserved aliases with no backing key
			}
			opts = append(opts, awsOption{Name: awssdk.ToString(a.AliasName), Value: target})
		}
	}
	return opts, nil
}

func listIAMRoles(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := iam.NewFromConfig(cfg)
	var opts []awsOption
	p := iam.NewListRolesPaginator(client, &iam.ListRolesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Roles {
			opts = append(opts, awsOption{Name: awssdk.ToString(r.RoleName), Value: awssdk.ToString(r.Arn)})
		}
	}
	return opts, nil
}

func listSNSTopics(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := sns.NewFromConfig(cfg)
	var opts []awsOption
	p := sns.NewListTopicsPaginator(client, &sns.ListTopicsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range page.Topics {
			arn := awssdk.ToString(t.TopicArn)
			name := arn
			if i := strings.LastIndex(arn, ":"); i >= 0 && i+1 < len(arn) {
				name = arn[i+1:]
			}
			opts = append(opts, awsOption{Name: name, Value: arn})
		}
	}
	return opts, nil
}

// listOrderableInstanceClasses returns the distinct DB instance classes orderable
// for the given engine in this region, powering the db_instance_class picker.
// DescribeOrderableDBInstanceOptions returns one row per class/version/AZ combo,
// so classes are de-duplicated. Bounded at 200 distinct classes.
func listOrderableInstanceClasses(ctx context.Context, cfg awssdk.Config, engine string) ([]awsOption, error) {
	client := rds.NewFromConfig(cfg)
	seen := map[string]bool{}
	var opts []awsOption
	p := rds.NewDescribeOrderableDBInstanceOptionsPaginator(client, &rds.DescribeOrderableDBInstanceOptionsInput{
		Engine: awssdk.String(engine),
	})
	for p.HasMorePages() && len(opts) < 200 {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.OrderableDBInstanceOptions {
			class := awssdk.ToString(o.DBInstanceClass)
			if class == "" || seen[class] {
				continue
			}
			seen[class] = true
			opts = append(opts, awsOption{Name: class, Value: class})
		}
	}
	return opts, nil
}

// ec2Label renders "<Name tag> (<id>)" when the resource has a Name tag, else the
// bare id — the same convention listSubnets/listSecurityGroups use.
func ec2Label(id string, tags []ec2types.Tag) string {
	for _, t := range tags {
		if awssdk.ToString(t.Key) == "Name" {
			if v := awssdk.ToString(t.Value); v != "" {
				return fmt.Sprintf("%s (%s)", v, id)
			}
		}
	}
	return id
}

func listVPCs(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeVpcsPaginator(client, &ec2.DescribeVpcsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range page.Vpcs {
			id := awssdk.ToString(v.VpcId)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", ec2Label(id, v.Tags), awssdk.ToString(v.CidrBlock)), Value: id})
		}
	}
	return opts, nil
}

func listRouteTables(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeRouteTablesPaginator(client, &ec2.DescribeRouteTablesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, rt := range page.RouteTables {
			id := awssdk.ToString(rt.RouteTableId)
			opts = append(opts, awsOption{Name: ec2Label(id, rt.Tags), Value: id})
		}
	}
	return opts, nil
}

func listInternetGateways(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeInternetGatewaysPaginator(client, &ec2.DescribeInternetGatewaysInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range page.InternetGateways {
			id := awssdk.ToString(g.InternetGatewayId)
			opts = append(opts, awsOption{Name: ec2Label(id, g.Tags), Value: id})
		}
	}
	return opts, nil
}

func listNatGateways(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeNatGatewaysPaginator(client, &ec2.DescribeNatGatewaysInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range page.NatGateways {
			id := awssdk.ToString(g.NatGatewayId)
			opts = append(opts, awsOption{Name: ec2Label(id, g.Tags), Value: id})
		}
	}
	return opts, nil
}

func listNetworkAcls(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeNetworkAclsPaginator(client, &ec2.DescribeNetworkAclsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range page.NetworkAcls {
			id := awssdk.ToString(a.NetworkAclId)
			opts = append(opts, awsOption{Name: ec2Label(id, a.Tags), Value: id})
		}
	}
	return opts, nil
}

func listVpcEndpoints(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeVpcEndpointsPaginator(client, &ec2.DescribeVpcEndpointsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range page.VpcEndpoints {
			id := awssdk.ToString(e.VpcEndpointId)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", ec2Label(id, e.Tags), awssdk.ToString(e.ServiceName)), Value: id})
		}
	}
	return opts, nil
}

func listVpcPeeringConnections(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeVpcPeeringConnectionsPaginator(client, &ec2.DescribeVpcPeeringConnectionsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, pc := range page.VpcPeeringConnections {
			id := awssdk.ToString(pc.VpcPeeringConnectionId)
			opts = append(opts, awsOption{Name: ec2Label(id, pc.Tags), Value: id})
		}
	}
	return opts, nil
}

func listTransitGateways(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeTransitGatewaysPaginator(client, &ec2.DescribeTransitGatewaysInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range page.TransitGateways {
			id := awssdk.ToString(g.TransitGatewayId)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", id, awssdk.ToString(g.Description)), Value: id})
		}
	}
	return opts, nil
}

func listDhcpOptions(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeDhcpOptionsPaginator(client, &ec2.DescribeDhcpOptionsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, d := range page.DhcpOptions {
			id := awssdk.ToString(d.DhcpOptionsId)
			opts = append(opts, awsOption{Name: ec2Label(id, d.Tags), Value: id})
		}
	}
	return opts, nil
}

func listNetworkInterfaces(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeNetworkInterfacesPaginator(client, &ec2.DescribeNetworkInterfacesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, ni := range page.NetworkInterfaces {
			id := awssdk.ToString(ni.NetworkInterfaceId)
			// NetworkInterface exposes its tags as TagSet, not Tags.
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", ec2Label(id, ni.TagSet), awssdk.ToString(ni.Description)), Value: id})
		}
	}
	return opts, nil
}

// listElasticIPs, listCustomerGateways and listVpnGateways have no paginator —
// single Describe call.
func listElasticIPs(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	out, err := client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
	if err != nil {
		return nil, err
	}
	var opts []awsOption
	for _, a := range out.Addresses {
		id := awssdk.ToString(a.AllocationId)
		if id == "" {
			continue // classic EIPs have no allocation id
		}
		opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", awssdk.ToString(a.PublicIp), ec2Label(id, a.Tags)), Value: id})
	}
	return opts, nil
}

func listCustomerGateways(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	out, err := client.DescribeCustomerGateways(ctx, &ec2.DescribeCustomerGatewaysInput{})
	if err != nil {
		return nil, err
	}
	var opts []awsOption
	for _, g := range out.CustomerGateways {
		id := awssdk.ToString(g.CustomerGatewayId)
		opts = append(opts, awsOption{Name: ec2Label(id, g.Tags), Value: id})
	}
	return opts, nil
}

func listVpnGateways(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	out, err := client.DescribeVpnGateways(ctx, &ec2.DescribeVpnGatewaysInput{})
	if err != nil {
		return nil, err
	}
	var opts []awsOption
	for _, g := range out.VpnGateways {
		id := awssdk.ToString(g.VpnGatewayId)
		opts = append(opts, awsOption{Name: ec2Label(id, g.Tags), Value: id})
	}
	return opts, nil
}

func listTransitGatewayAttachments(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeTransitGatewayAttachmentsPaginator(client, &ec2.DescribeTransitGatewayAttachmentsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range page.TransitGatewayAttachments {
			id := awssdk.ToString(a.TransitGatewayAttachmentId)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", ec2Label(id, a.Tags), string(a.ResourceType)), Value: id})
		}
	}
	return opts, nil
}

func listTransitGatewayRouteTables(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeTransitGatewayRouteTablesPaginator(client, &ec2.DescribeTransitGatewayRouteTablesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, rt := range page.TransitGatewayRouteTables {
			id := awssdk.ToString(rt.TransitGatewayRouteTableId)
			opts = append(opts, awsOption{Name: ec2Label(id, rt.Tags), Value: id})
		}
	}
	return opts, nil
}

func listVpcEndpointServices(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeVpcEndpointServiceConfigurationsPaginator(client, &ec2.DescribeVpcEndpointServiceConfigurationsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range page.ServiceConfigurations {
			id := awssdk.ToString(s.ServiceId)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", ec2Label(id, s.Tags), awssdk.ToString(s.ServiceName)), Value: id})
		}
	}
	return opts, nil
}
