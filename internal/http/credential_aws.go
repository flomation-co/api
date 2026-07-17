package http

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// createAWSRoleCredential provisions a token-less "AWS Role" credential. Rather
// than an OAuth round-trip, it generates a per-credential External ID, stores
// {role_arn, external_id, region} in the credential metadata (marked active
// immediately), and returns the trust policy the customer must attach to their
// role so Flomation's principal can assume it.
func (s *Service) createAWSRoleCredential(c *gin.Context, environmentID string, req createCredentialRequest) {
	roleARN := strings.TrimSpace(req.RoleARN)
	if !validRoleARN(roleARN) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid IAM role ARN is required (arn:aws:iam::<account>:role/<name>)"})
		return
	}

	externalID := generateExternalID()

	metaBytes, err := json.Marshal(map[string]interface{}{
		"role_arn":    roleARN,
		"external_id": externalID,
		"region":      strings.TrimSpace(req.Region),
	})
	if err != nil {
		log.WithError(err).Error("unable to encode aws_role credential metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	credID, err := s.persistence.CreateAWSRoleCredential(environmentID, req.Name, json.RawMessage(metaBytes))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			c.JSON(http.StatusConflict, gin.H{"error": "credential name already exists in this environment"})
			return
		}
		log.WithError(err).Error("unable to create aws_role credential")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	trustARN := s.awsTrustPrincipalARN()
	c.JSON(http.StatusCreated, gin.H{
		"id":                  credID,
		"external_id":         externalID,
		"trust_principal_arn": trustARN,
		"trust_policy":        buildTrustPolicy(trustARN, externalID),
	})
}

// getPlatformConfig exposes non-secret platform-level values the editor needs at
// render time — currently the AWS trust principal ARN shown in the AWS Role
// credential help.
func (s *Service) getPlatformConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"aws_trust_principal_arn": s.awsTrustPrincipalARN(),
	})
}

// awsTrustPrincipalARN returns the configured Flomation principal customers must
// trust, or a clearly-marked placeholder when it hasn't been set yet.
func (s *Service) awsTrustPrincipalARN() string {
	if s.config != nil && s.config.AWS != nil && strings.TrimSpace(s.config.AWS.TrustPrincipalARN) != "" {
		return s.config.AWS.TrustPrincipalARN
	}
	return "arn:aws:iam::<FLOMATION_ACCOUNT_ID>:role/flomation-executor"
}

// validRoleARN does a lightweight shape check on an IAM role ARN.
func validRoleARN(arn string) bool {
	return strings.HasPrefix(arn, "arn:aws:iam::") && strings.Contains(arn, ":role/")
}

// generateExternalID mints an unguessable, unique External ID. Uniqueness +
// unpredictability is what makes the confused-deputy protection meaningful.
func generateExternalID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "flomation-" + hex.EncodeToString(b)
}

// buildTrustPolicy renders the IAM role trust policy the customer pastes: it
// allows Flomation's principal to assume the role only when presenting the
// matching External ID.
func buildTrustPolicy(principalARN, externalID string) string {
	policy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{{
			"Effect":    "Allow",
			"Principal": map[string]string{"AWS": principalARN},
			"Action":    "sts:AssumeRole",
			"Condition": map[string]interface{}{
				"StringEquals": map[string]string{"sts:ExternalId": externalID},
			},
		}},
	}
	b, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\": %q}", err.Error())
	}
	return string(b)
}
