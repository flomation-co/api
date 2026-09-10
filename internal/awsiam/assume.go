package awsiam

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AssumeRoleConfig returns an aws.Config whose credentials are obtained by
// assuming roleARN (with externalID) using the credential's OWN base keys as the
// STS principal — the same identity the executor uses at run time. It lets
// server-side helpers (e.g. the resource-autocomplete proxy) act as a managed
// AWS Role credential without ever exposing keys to the client.
//
// The returned config carries an auto-refreshing credentials cache, so a single
// config can back multiple/paginated calls. region applies to both STS and the
// downstream service clients; it defaults to us-east-1 when empty.
func AssumeRoleConfig(ctx context.Context, accessKeyID, secretKey, region, roleARN, externalID string) (aws.Config, error) {
	if region == "" {
		region = "us-east-1"
	}
	base, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: aws.Credentials{AccessKeyID: accessKeyID, SecretAccessKey: secretKey, Source: "Flomation aws_role base"},
		}),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load base config: %w", err)
	}
	stsClient := sts.NewFromConfig(base)
	provider := stscreds.NewAssumeRoleProvider(stsClient, roleARN, func(o *stscreds.AssumeRoleOptions) {
		if externalID != "" {
			o.ExternalID = aws.String(externalID)
		}
	})
	base.Credentials = aws.NewCredentialsCache(provider)
	return base, nil
}
