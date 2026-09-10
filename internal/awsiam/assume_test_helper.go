package awsiam

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// TestAssumeRole verifies that a credential's dedicated Flomation identity can
// actually assume the customer's role — i.e. that the trust policy is attached,
// the External ID matches, and the role exists. It uses the credential's OWN
// base keys (not the provisioning identity), exactly as the executor will at run
// time.
//
// A freshly-minted access key can take a few seconds to become usable (IAM is
// eventually consistent), so InvalidClientTokenId / access-key-not-found errors
// are retried briefly before giving up.
func TestAssumeRole(ctx context.Context, accessKeyID, secretKey, region, roleARN, externalID string) error {
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: aws.Credentials{
				AccessKeyID:     accessKeyID,
				SecretAccessKey: secretKey,
				Source:          "Flomation AWS credential test",
			},
		}),
	)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	client := sts.NewFromConfig(cfg)

	in := &sts.AssumeRoleInput{
		RoleArn:         aws.String(roleARN),
		RoleSessionName: aws.String("flomation-credential-test"),
	}
	if externalID != "" {
		in.ExternalId = aws.String(externalID)
	}

	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if _, err := client.AssumeRole(ctx, in); err == nil {
			return nil
		} else {
			lastErr = err
			// Retry only the "key not propagated yet" class of error.
			if !isTransientCredError(err) {
				return err
			}
		}
		time.Sleep(3 * time.Second)
	}
	return lastErr
}

func isTransientCredError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "InvalidClientTokenId") ||
		strings.Contains(msg, "security token included in the request is invalid") ||
		strings.Contains(msg, "SignatureDoesNotMatch")
}
