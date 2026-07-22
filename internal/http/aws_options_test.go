package http

import (
	"testing"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/gomega"
)

func TestAWSDynamicOption(t *testing.T) {
	g := NewWithT(t)

	// An aws/* action's resource input gets the matching picker.
	dyn, ok := awsDynamicOption("aws/rds/create_db_instance", "vpc_security_group_ids")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-security-groups"))
	g.Expect(dyn.Params).To(ContainElement("aws_region"))
	g.Expect(dyn.Params).To(ContainElement("aws_access_key"))
	g.Expect(dyn.Params).To(ContainElement("environment"))

	dyn, ok = awsDynamicOption("aws/rds/create_db_cluster", "kms_key_id")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-kms-keys"))

	dyn, ok = awsDynamicOption("aws/ec2/run_instances", "sns_topic_arn")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-sns-topics"))

	// db_instance_class is an engine/region-aware picker (DescribeOrderable).
	dyn, ok = awsDynamicOption("aws/rds/create_db_instance", "db_instance_class")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-db-instance-classes"))
	g.Expect(dyn.Params).To(ContainElement("engine"))

	// EC2/S3 resource inputs added alongside the rounded-out EC2 + S3 suites.
	dyn, ok = awsDynamicOption("aws/ec2/attach_volume", "volume_id")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-volumes"))

	dyn, ok = awsDynamicOption("aws/s3/put_bucket_policy", "bucket")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-s3-buckets"))

	dyn, ok = awsDynamicOption("aws/ec2/deregister_image", "image_id")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-images"))

	// ELBv2 + Auto Scaling pickers.
	dyn, ok = awsDynamicOption("aws/elbv2/create_listener", "load_balancer_arn")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-load-balancers"))

	dyn, ok = awsDynamicOption("aws/elbv2/register_targets", "target_group_arn")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-target-groups"))

	dyn, ok = awsDynamicOption("aws/autoscaling/set_desired_capacity", "auto_scaling_group_name")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-asgs"))

	// Route 53 pickers.
	dyn, ok = awsDynamicOption("aws/route53/change_resource_record_sets", "hosted_zone_id")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-hosted-zones"))

	dyn, ok = awsDynamicOption("aws/route53/get_health_check", "health_check_id")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-health-checks"))

	// CloudWatch pickers.
	dyn, ok = awsDynamicOption("aws/cloudwatch/set_alarm_state", "alarm_name")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-alarms"))

	dyn, ok = awsDynamicOption("aws/cloudwatchlogs/filter_log_events", "log_group_name")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-log-groups"))

	// IAM + Secrets pickers.
	dyn, ok = awsDynamicOption("aws/secretsmanager/get_secret_value", "secret_id")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-secrets"))

	dyn, ok = awsDynamicOption("aws/iam/attach_user_policy", "policy_arn")
	g.Expect(ok).To(BeTrue())
	g.Expect(dyn.Endpoint).To(Equal("/api/v1/action/options/aws-iam-policies"))

	// A genuinely non-resource input on an aws/* action gets nothing.
	_, ok = awsDynamicOption("aws/rds/create_db_instance", "master_username")
	g.Expect(ok).To(BeFalse())

	// A resource-named input on a NON-aws action gets nothing (rule is scoped).
	_, ok = awsDynamicOption("azure/compute/vm_create", "kms_key_id")
	g.Expect(ok).To(BeFalse())
}

// Every picker slug in awsResourceInputs must be handled by awsOptions' switch,
// or the route would 200 with {"error":"unknown AWS resource list"}. This guards
// against adding an input mapping without a lister.
func TestAWSResourceSlugsAllHandled(t *testing.T) {
	g := NewWithT(t)
	handled := map[string]bool{
		"security-groups": true, "subnets": true, "subnet-groups": true,
		"kms-keys": true, "iam-roles": true, "sns-topics": true,
		"db-instance-classes": true,
		// VPC networking pickers
		"vpcs": true, "route-tables": true, "internet-gateways": true,
		"nat-gateways": true, "network-acls": true, "vpc-endpoints": true,
		"vpc-peering-connections": true, "transit-gateways": true, "elastic-ips": true,
		"dhcp-options": true, "network-interfaces": true, "customer-gateways": true,
		"vpn-gateways":                true,
		"transit-gateway-attachments": true, "transit-gateway-route-tables": true,
		"vpc-endpoint-services": true,
		// EC2 compute + S3 pickers
		"volumes": true, "snapshots": true, "images": true, "key-pairs": true,
		"s3-buckets": true,
		// ELBv2 + Auto Scaling pickers
		"load-balancers": true, "target-groups": true, "asgs": true,
		// Route 53 pickers
		"hosted-zones": true, "health-checks": true,
		// CloudWatch pickers
		"alarms": true, "log-groups": true,
		// IAM + Secrets pickers
		"secrets": true, "iam-users": true, "iam-groups": true, "iam-policies": true,
	}
	for input, slug := range awsResourceInputs {
		g.Expect(handled[slug]).To(BeTrue(), "input %q maps to slug %q which awsOptions doesn't handle", input, slug)
	}
}

// Regression: several inputs map to the same picker slug (e.g. subnet_id and
// subnet_ids both → "subnets"), so route registration MUST dedupe by slug — gin
// panics on a duplicate path. This reproduces the crash without dedup and proves
// the deduped registration is panic-free.
func TestAWSOptionRoutesDedupeBySlug(t *testing.T) {
	g := NewWithT(t)
	gin.SetMode(gin.TestMode)

	// There genuinely are duplicate slug values, else the guard is meaningless.
	unique := map[string]bool{}
	for _, slug := range awsResourceInputs {
		unique[slug] = true
	}
	g.Expect(len(unique)).To(BeNumerically("<", len(awsResourceInputs)),
		"expected some input names to share a slug (the condition the dedup guards)")

	noop := func(c *gin.Context) {}

	// Without dedup: registering one route per input name collides → panic (the bug).
	g.Expect(func() {
		grp := gin.New().Group("/api/v1/action")
		for _, slug := range awsResourceInputs {
			grp.GET("/options/aws-"+slug, noop)
		}
	}).To(Panic())

	// With dedup (as service.go does): one route per unique slug → no panic.
	g.Expect(func() {
		grp := gin.New().Group("/api/v1/action")
		seen := map[string]bool{}
		for _, slug := range awsResourceInputs {
			if seen[slug] {
				continue
			}
			seen[slug] = true
			grp.GET("/options/aws-"+slug, noop)
		}
	}).ToNot(Panic())
}
