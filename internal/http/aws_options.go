package http

import (
	"context"
	"encoding/json"
	"fmt"
	gohttp "net/http"
	"sort"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/gin-gonic/gin"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/awsiam"
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
	// EC2 compute resources (aws/ec2/* — volumes, snapshots, AMIs, key pairs).
	// These map the *current-region* resource inputs only; the copy actions'
	// source_* inputs deliberately have no picker (they name resources in a
	// different, user-supplied source region).
	"volume_id":   "volumes",
	"snapshot_id": "snapshots",
	"image_id":    "images",
	"key_name":    "key-pairs",
	// S3 (aws/s3/* — the bucket is the primary resource users wire).
	"bucket": "s3-buckets",
	// Elastic Load Balancing + Auto Scaling (aws/elbv2/*, aws/autoscaling/*).
	"load_balancer_arn":       "load-balancers",
	"target_group_arn":        "target-groups",
	"auto_scaling_group_name": "asgs",
	// Route 53 (aws/route53/*).
	"hosted_zone_id":  "hosted-zones",
	"health_check_id": "health-checks",
	// CloudWatch (aws/cloudwatch/*, aws/cloudwatchlogs/*).
	"alarm_name":     "alarms",
	"log_group_name": "log-groups",
	// IAM + Secrets Manager (aws/iam/*, aws/secretsmanager/*). kms_key_id and
	// role_arn already have pickers (kms-keys / iam-roles).
	"secret_id":  "secrets",
	"user_name":  "iam-users",
	"group_name": "iam-groups",
	"policy_arn": "iam-policies",
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

	// Managed AWS Role credential: when one is selected the editor auto-fills the
	// key params with ${credentials.X.base_access_key_id} / ${credentials.X}
	// accessors (so they're NON-empty), so detect the managed case up front — from
	// the credential input or the secret-key accessor — and assume the role
	// server-side rather than trying to resolve those accessors as plain secrets.
	if name := managedCredFromQuery(c); name != "" {
		return s.resolveManagedAWSConfig(c, name, region)
	}

	accessRaw := strings.TrimSpace(c.Query("aws_access_key"))
	secretRaw := strings.TrimSpace(c.Query("aws_secret_key"))

	if accessRaw == "" || secretRaw == "" {
		if strings.TrimSpace(c.Query("assume_role_arn")) != "" {
			return awssdk.Config{}, "Assume-role auth can't load this list — pick with access keys, or type the value in (the flow still runs)", false
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

// resolveManagedAWSConfig resolves a ${credentials.X} managed AWS Role credential
// and returns an aws.Config that ASSUMES its role — so resource autocomplete works
// with managed credentials, not just pasted access keys. The credential's base
// secret is used server-side only (never returned to the client; only resource
// names/ids are). Gated by EnvironmentView + a user-scoped environment lookup,
// exactly like resolveAWSSecretParam, so a requester can only resolve credentials
// in an environment they can already view.
func (s *Service) resolveManagedAWSConfig(c *gin.Context, name, region string) (awssdk.Config, string, bool) {
	environmentID := strings.TrimSpace(c.Query("environment"))
	if environmentID == "" {
		return awssdk.Config{}, "Select an environment to load this list", false
	}
	if !s.checkPermission(c, rbac.EnvironmentView) {
		return awssdk.Config{}, "", false // checkPermission wrote the response
	}

	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}
	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		return awssdk.Config{}, "Environment not found", false
	}

	baseSecret, metaRaw, err := s.persistence.GetCredentialWithMetaByName(environmentID, name, env.SecretKey)
	if err != nil || baseSecret == nil || metaRaw == nil {
		return awssdk.Config{}, "Couldn't load this list from the credential — pick with access keys, or type the value in", false
	}
	var meta struct {
		BaseAccessKeyID string `json:"base_access_key_id"`
		ExternalID      string `json:"external_id"`
		Region          string `json:"region"`
		RoleARN         string `json:"role_arn"`
	}
	if err := json.Unmarshal(*metaRaw, &meta); err != nil || meta.BaseAccessKeyID == "" {
		return awssdk.Config{}, "This isn't an AWS Role credential — pick with access keys, or type the value in", false
	}
	if strings.TrimSpace(meta.RoleARN) == "" {
		return awssdk.Config{}, "Finish the AWS Role credential setup (attach a role ARN) to load this list", false
	}

	reg := region
	if reg == "" {
		reg = meta.Region
	}
	cfg, err := awsiam.AssumeRoleConfig(c.Request.Context(), meta.BaseAccessKeyID, *baseSecret, reg, meta.RoleARN, meta.ExternalID)
	if err != nil {
		return awssdk.Config{}, "Couldn't assume the credential's role: " + err.Error(), false
	}
	return cfg, "", true
}

// managedCredFromQuery returns the managed AWS Role credential name referenced by
// the request, or "" if this isn't a managed-credential request. The editor sets
// both the `credential` input and the auto-filled `aws_secret_key` to the plain
// ${credentials.NAME} form (aws_access_key gets the .base_access_key_id accessor,
// so it's deliberately not consulted here).
func managedCredFromQuery(c *gin.Context) string {
	for _, p := range []string{"credential", "aws_secret_key"} {
		if name := managedCredentialName(strings.TrimSpace(c.Query(p))); name != "" {
			return name
		}
	}
	return ""
}

// managedCredentialName extracts NAME from "${credentials.NAME}" / "${credential.NAME}",
// or "" if raw isn't that form. Credential names are already sanitised to [A-Za-z0-9_-].
func managedCredentialName(raw string) string {
	v := strings.TrimSpace(raw)
	for _, p := range []string{"${credentials.", "${credential."} {
		if strings.HasPrefix(v, p) && strings.HasSuffix(v, "}") {
			return strings.TrimSpace(v[len(p) : len(v)-1])
		}
	}
	return ""
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
		case "volumes":
			opts, err = listVolumes(ctx, cfg)
		case "snapshots":
			opts, err = listSnapshots(ctx, cfg)
		case "images":
			opts, err = listImages(ctx, cfg)
		case "key-pairs":
			opts, err = listKeyPairs(ctx, cfg)
		case "s3-buckets":
			opts, err = listS3Buckets(ctx, cfg)
		case "load-balancers":
			opts, err = listLoadBalancers(ctx, cfg)
		case "target-groups":
			opts, err = listTargetGroups(ctx, cfg)
		case "asgs":
			opts, err = listAutoScalingGroups(ctx, cfg)
		case "hosted-zones":
			opts, err = listHostedZones(ctx, cfg)
		case "health-checks":
			opts, err = listHealthChecks(ctx, cfg)
		case "alarms":
			opts, err = listAlarms(ctx, cfg)
		case "log-groups":
			opts, err = listLogGroups(ctx, cfg)
		case "secrets":
			opts, err = listSecrets(ctx, cfg)
		case "iam-users":
			opts, err = listIAMUsers(ctx, cfg)
		case "iam-groups":
			opts, err = listIAMGroups(ctx, cfg)
		case "iam-policies":
			opts, err = listIAMPolicies(ctx, cfg)
		default:
			c.JSON(gohttp.StatusOK, gin.H{"error": "unknown AWS resource list"})
			return
		}
		if err != nil {
			c.JSON(gohttp.StatusOK, gin.H{"error": err.Error()})
			return
		}
		sort.Slice(opts, func(i, j int) bool { return strings.ToLower(opts[i].Name) < strings.ToLower(opts[j].Name) })
		// Serialise an empty result as [] not null: a lister that matched nothing
		// (e.g. an account with no self-owned AMIs / key pairs) leaves opts nil,
		// which JSON-encodes as `null` — the editor treats a non-array as "Failed
		// to load options" rather than an empty dropdown. This is a successful,
		// empty result, so return an empty list.
		if opts == nil {
			opts = []awsOption{}
		}
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

func listVolumes(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	p := ec2.NewDescribeVolumesPaginator(client, &ec2.DescribeVolumesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range page.Volumes {
			id := awssdk.ToString(v.VolumeId)
			size := awssdk.ToInt32(v.Size)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %d GiB, %s", ec2Label(id, v.Tags), size, string(v.State)), Value: id})
		}
	}
	return opts, nil
}

func listSnapshots(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	var opts []awsOption
	// Scope to snapshots this account owns — the public/shared pool is enormous.
	p := ec2.NewDescribeSnapshotsPaginator(client, &ec2.DescribeSnapshotsInput{OwnerIds: []string{"self"}})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range page.Snapshots {
			id := awssdk.ToString(s.SnapshotId)
			desc := awssdk.ToString(s.Description)
			label := ec2Label(id, s.Tags)
			if desc != "" {
				label = fmt.Sprintf("%s — %s", label, desc)
			}
			opts = append(opts, awsOption{Name: label, Value: id})
		}
	}
	return opts, nil
}

func listImages(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	// Self-owned AMIs only (the public catalogue is far too large to list).
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{Owners: []string{"self"}})
	if err != nil {
		return nil, err
	}
	var opts []awsOption
	for _, img := range out.Images {
		id := awssdk.ToString(img.ImageId)
		name := awssdk.ToString(img.Name)
		label := ec2Label(id, img.Tags)
		if name != "" {
			label = fmt.Sprintf("%s — %s", name, id)
		}
		opts = append(opts, awsOption{Name: label, Value: id})
	}
	return opts, nil
}

func listKeyPairs(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := ec2.NewFromConfig(cfg)
	out, err := client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{})
	if err != nil {
		return nil, err
	}
	var opts []awsOption
	for _, kp := range out.KeyPairs {
		name := awssdk.ToString(kp.KeyName)
		// The key pair's Value is its name (that's what create/delete/import take).
		opts = append(opts, awsOption{Name: name, Value: name})
	}
	return opts, nil
}

func listS3Buckets(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := s3.NewFromConfig(cfg)
	out, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, err
	}
	var opts []awsOption
	for _, b := range out.Buckets {
		name := awssdk.ToString(b.Name)
		opts = append(opts, awsOption{Name: name, Value: name})
	}
	return opts, nil
}

func listLoadBalancers(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := elasticloadbalancingv2.NewFromConfig(cfg)
	var opts []awsOption
	p := elasticloadbalancingv2.NewDescribeLoadBalancersPaginator(client, &elasticloadbalancingv2.DescribeLoadBalancersInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, lb := range page.LoadBalancers {
			arn := awssdk.ToString(lb.LoadBalancerArn)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", awssdk.ToString(lb.LoadBalancerName), string(lb.Type)), Value: arn})
		}
	}
	return opts, nil
}

func listTargetGroups(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := elasticloadbalancingv2.NewFromConfig(cfg)
	var opts []awsOption
	p := elasticloadbalancingv2.NewDescribeTargetGroupsPaginator(client, &elasticloadbalancingv2.DescribeTargetGroupsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, tg := range page.TargetGroups {
			arn := awssdk.ToString(tg.TargetGroupArn)
			label := awssdk.ToString(tg.TargetGroupName)
			if tg.Protocol != "" {
				label = fmt.Sprintf("%s — %s:%d", label, string(tg.Protocol), awssdk.ToInt32(tg.Port))
			}
			opts = append(opts, awsOption{Name: label, Value: arn})
		}
	}
	return opts, nil
}

func listAutoScalingGroups(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := autoscaling.NewFromConfig(cfg)
	var opts []awsOption
	p := autoscaling.NewDescribeAutoScalingGroupsPaginator(client, &autoscaling.DescribeAutoScalingGroupsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range page.AutoScalingGroups {
			name := awssdk.ToString(g.AutoScalingGroupName)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %d/%d/%d (min/desired/max)", name, awssdk.ToInt32(g.MinSize), awssdk.ToInt32(g.DesiredCapacity), awssdk.ToInt32(g.MaxSize)), Value: name})
		}
	}
	return opts, nil
}

func listHostedZones(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := route53.NewFromConfig(cfg)
	var opts []awsOption
	p := route53.NewListHostedZonesPaginator(client, &route53.ListHostedZonesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, z := range page.HostedZones {
			// The zone id comes back as "/hostedzone/Z123"; strip the prefix so
			// the value matches what the actions expect.
			id := strings.TrimPrefix(awssdk.ToString(z.Id), "/hostedzone/")
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", awssdk.ToString(z.Name), id), Value: id})
		}
	}
	return opts, nil
}

func listHealthChecks(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := route53.NewFromConfig(cfg)
	var opts []awsOption
	p := route53.NewListHealthChecksPaginator(client, &route53.ListHealthChecksInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, hc := range page.HealthChecks {
			id := awssdk.ToString(hc.Id)
			label := id
			if hc.HealthCheckConfig != nil {
				if fqdn := awssdk.ToString(hc.HealthCheckConfig.FullyQualifiedDomainName); fqdn != "" {
					label = fmt.Sprintf("%s — %s", fqdn, id)
				} else if ip := awssdk.ToString(hc.HealthCheckConfig.IPAddress); ip != "" {
					label = fmt.Sprintf("%s — %s", ip, id)
				}
			}
			opts = append(opts, awsOption{Name: label, Value: id})
		}
	}
	return opts, nil
}

func listAlarms(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := cloudwatch.NewFromConfig(cfg)
	var opts []awsOption
	p := cloudwatch.NewDescribeAlarmsPaginator(client, &cloudwatch.DescribeAlarmsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range page.MetricAlarms {
			name := awssdk.ToString(a.AlarmName)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", name, string(a.StateValue)), Value: name})
		}
		for _, a := range page.CompositeAlarms {
			name := awssdk.ToString(a.AlarmName)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s (composite)", name, string(a.StateValue)), Value: name})
		}
	}
	return opts, nil
}

func listLogGroups(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := cloudwatchlogs.NewFromConfig(cfg)
	var opts []awsOption
	p := cloudwatchlogs.NewDescribeLogGroupsPaginator(client, &cloudwatchlogs.DescribeLogGroupsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range page.LogGroups {
			name := awssdk.ToString(g.LogGroupName)
			opts = append(opts, awsOption{Name: name, Value: name})
		}
	}
	return opts, nil
}

func listSecrets(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := secretsmanager.NewFromConfig(cfg)
	var opts []awsOption
	p := secretsmanager.NewListSecretsPaginator(client, &secretsmanager.ListSecretsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range page.SecretList {
			name := awssdk.ToString(s.Name)
			// The action inputs accept a name or ARN as secret_id; the name is friendlier.
			opts = append(opts, awsOption{Name: name, Value: name})
		}
	}
	return opts, nil
}

func listIAMUsers(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := iam.NewFromConfig(cfg)
	var opts []awsOption
	p := iam.NewListUsersPaginator(client, &iam.ListUsersInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, u := range page.Users {
			name := awssdk.ToString(u.UserName)
			opts = append(opts, awsOption{Name: name, Value: name})
		}
	}
	return opts, nil
}

func listIAMGroups(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := iam.NewFromConfig(cfg)
	var opts []awsOption
	p := iam.NewListGroupsPaginator(client, &iam.ListGroupsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range page.Groups {
			name := awssdk.ToString(g.GroupName)
			opts = append(opts, awsOption{Name: name, Value: name})
		}
	}
	return opts, nil
}

func listIAMPolicies(ctx context.Context, cfg awssdk.Config) ([]awsOption, error) {
	client := iam.NewFromConfig(cfg)
	var opts []awsOption
	// Customer-managed policies only — the AWS-managed pool is enormous.
	p := iam.NewListPoliciesPaginator(client, &iam.ListPoliciesInput{Scope: iamtypes.PolicyScopeTypeLocal})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, pol := range page.Policies {
			arn := awssdk.ToString(pol.Arn)
			opts = append(opts, awsOption{Name: fmt.Sprintf("%s — %s", awssdk.ToString(pol.PolicyName), arn), Value: arn})
		}
	}
	return opts, nil
}
