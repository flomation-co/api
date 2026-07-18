// Package awsiam auto-provisions a dedicated, tightly-scoped IAM user for each
// AWS Role credential in Flomation's own AWS account.
//
// Each credential gets its own Flomation IAM user, so the base identity used to
// STS AssumeRole into the customer's role is unique per credential. A compromised
// execution therefore only ever holds one credential's keys and can assume only
// that credential's role — never another tenant's. The minted user is capped by
// a permissions boundary to sts:AssumeRole only, and is created under a fixed
// path so the provisioning identity's own IAM permissions can be scoped to that
// path. The provisioning credentials live only in the API, never on the runner.
package awsiam

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"flomation.app/automate/api/internal/config"
)

// Provisioner mints and removes per-credential IAM users.
type Provisioner struct {
	client   *iam.Client
	path     string
	boundary string
}

// Identity is the newly-minted user's ARN and access keys.
type Identity struct {
	UserName        string
	UserARN         string
	AccessKeyID     string
	SecretAccessKey string
}

// NewProvisioner builds a Provisioner from config, or returns (nil, nil) when
// provisioning is not configured (the caller then falls back to the
// single-principal model).
func NewProvisioner(cfg *config.AWSProvisioningConfig) (*Provisioner, error) {
	if cfg == nil || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, nil
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1" // IAM is global; any region works for the endpoint.
	}
	awscfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: aws.Credentials{
				AccessKeyID:     cfg.AccessKeyID,
				SecretAccessKey: cfg.SecretAccessKey,
				Source:          "Flomation AWS provisioning",
			},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("load provisioning aws config: %w", err)
	}
	path := cfg.UserPath
	if path == "" {
		path = "/flomation-creds/"
	}
	return &Provisioner{
		client:   iam.NewFromConfig(awscfg),
		path:     path,
		boundary: cfg.PermissionsBoundaryARN,
	}, nil
}

// CreateCredentialIdentity creates a boundary-capped IAM user named userName,
// grants it sts:AssumeRole on exactly roleARN via an inline policy, and issues
// an access key. On any failure it best-effort cleans up the partial user so a
// retry starts clean.
func (p *Provisioner) CreateCredentialIdentity(ctx context.Context, userName, roleARN string) (*Identity, error) {
	createIn := &iam.CreateUserInput{
		UserName: aws.String(userName),
		Path:     aws.String(p.path),
	}
	if p.boundary != "" {
		createIn.PermissionsBoundary = aws.String(p.boundary)
	}
	out, err := p.client.CreateUser(ctx, createIn)
	if err != nil {
		return nil, fmt.Errorf("create iam user: %w", err)
	}
	userARN := aws.ToString(out.User.Arn)

	// Inline policy: this user may assume ONLY the customer role this credential
	// targets. The boundary independently caps it to sts:AssumeRole regardless.
	policyDoc, _ := json.Marshal(map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{{
			"Effect":   "Allow",
			"Action":   "sts:AssumeRole",
			"Resource": roleARN,
		}},
	})
	if _, err := p.client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(userName),
		PolicyName:     aws.String("assume-target-role"),
		PolicyDocument: aws.String(string(policyDoc)),
	}); err != nil {
		_ = p.DeleteCredentialIdentity(ctx, userName)
		return nil, fmt.Errorf("attach assume-role policy: %w", err)
	}

	key, err := p.client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(userName)})
	if err != nil {
		_ = p.DeleteCredentialIdentity(ctx, userName)
		return nil, fmt.Errorf("create access key: %w", err)
	}

	return &Identity{
		UserName:        userName,
		UserARN:         userARN,
		AccessKeyID:     aws.ToString(key.AccessKey.AccessKeyId),
		SecretAccessKey: aws.ToString(key.AccessKey.SecretAccessKey),
	}, nil
}

// DeleteCredentialIdentity removes a minted user and everything attached to it,
// so a deleted credential leaves no orphaned IAM identity. Each step is
// best-effort — a missing sub-resource must not block the others.
func (p *Provisioner) DeleteCredentialIdentity(ctx context.Context, userName string) error {
	if keys, err := p.client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(userName)}); err == nil {
		for _, k := range keys.AccessKeyMetadata {
			_, _ = p.client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
				UserName:    aws.String(userName),
				AccessKeyId: k.AccessKeyId,
			})
		}
	}
	_, _ = p.client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{
		UserName:   aws.String(userName),
		PolicyName: aws.String("assume-target-role"),
	})
	_, err := p.client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(userName)})
	return err
}
