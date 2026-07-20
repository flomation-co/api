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
