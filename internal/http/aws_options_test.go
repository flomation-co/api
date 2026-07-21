package http

import (
	"testing"

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
	}
	for input, slug := range awsResourceInputs {
		g.Expect(handled[slug]).To(BeTrue(), "input %q maps to slug %q which awsOptions doesn't handle", input, slug)
	}
}
