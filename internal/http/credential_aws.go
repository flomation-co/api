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

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/awsiam"
)

// createAWSRoleCredential provisions an "AWS Role" credential with its OWN
// dedicated Flomation IAM user (auto-provisioned, capped by a permissions
// boundary to sts:AssumeRole only). It generates a per-credential External ID,
// mints the IAM user + access key, stores the user's SECRET key encrypted with
// the credential and its non-secret fields in metadata, and returns the
// per-credential trust ARN + trust policy for the customer to attach to their
// role. The dedicated identity means a compromised execution using this
// credential can only ever assume this credential's role.
func (s *Service) createAWSRoleCredential(c *gin.Context, environmentID string, env *api.Environment, req createCredentialRequest) {
	// Role ARN is OPTIONAL at creation: the wizard mints Flomation's identity
	// first (step 1), the customer builds their role from the returned policies
	// (step 2), then attaches the ARN via setAWSRoleARN. When absent, the minted
	// user is granted sts:AssumeRole on "*" (capped to assume-role by the
	// boundary; trust is gated by the customer's role either way).
	roleARN := strings.TrimSpace(req.RoleARN)
	if roleARN != "" && !validRoleARN(roleARN) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IAM role ARN (arn:aws:iam::<account>:role/<name>)"})
		return
	}
	inlineResource := roleARN
	if inlineResource == "" {
		inlineResource = "*"
	}

	prov, err := s.awsProvisioner()
	if err != nil {
		log.WithError(err).Error("unable to build AWS provisioner")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if prov == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AWS provisioning is not configured on this server"})
		return
	}

	externalID := generateExternalID()
	userName := generateCredUserName()

	identity, err := prov.CreateCredentialIdentity(c.Request.Context(), userName, inlineResource)
	if err != nil {
		log.WithError(err).Error("unable to provision AWS credential identity")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to provision AWS identity: " + err.Error()})
		return
	}

	metaBytes, err := json.Marshal(map[string]interface{}{
		"role_arn":           roleARN,
		"external_id":        externalID,
		"region":             strings.TrimSpace(req.Region),
		"iam_user_arn":       identity.UserARN,
		"iam_user_name":      identity.UserName,
		"base_access_key_id": identity.AccessKeyID,
	})
	if err != nil {
		_ = prov.DeleteCredentialIdentity(c.Request.Context(), userName)
		log.WithError(err).Error("unable to encode aws_role credential metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	credID, err := s.persistence.CreateAWSRoleCredential(environmentID, req.Name, env.SecretKey, identity.SecretAccessKey, json.RawMessage(metaBytes))
	if err != nil {
		// Roll back the freshly-minted IAM user so a failed insert doesn't orphan it.
		_ = prov.DeleteCredentialIdentity(c.Request.Context(), userName)
		if strings.Contains(err.Error(), "duplicate key") {
			c.JSON(http.StatusConflict, gin.H{"error": "credential name already exists in this environment"})
			return
		}
		log.WithError(err).Error("unable to create aws_role credential")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":                  credID,
		"external_id":         externalID,
		"trust_principal_arn": identity.UserARN,
		"trust_policy":        buildTrustPolicy(identity.UserARN, externalID),
	})
}

// setAWSRoleARN is step 2 of the wizard: attach the customer role ARN (created
// from the policies shown in the UI) to an existing aws_role credential.
func (s *Service) setAWSRoleARN(c *gin.Context) {
	credID := c.Param("id")

	var body struct {
		RoleARN string `json:"role_arn"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	roleARN := strings.TrimSpace(body.RoleARN)
	if !validRoleARN(roleARN) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid IAM role ARN is required"})
		return
	}

	cred, err := s.persistence.GetCredentialByID(credID)
	if err != nil || cred == nil || cred.ProviderSlug != "aws_role" {
		c.JSON(http.StatusNotFound, gin.H{"error": "AWS role credential not found"})
		return
	}

	merged, err := api.MergeMetadata(cred.Metadata, map[string]interface{}{"role_arn": roleARN})
	if err != nil {
		log.WithError(err).Error("unable to merge aws_role metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := s.persistence.UpdateCredentialMetadata(credID, merged); err != nil {
		log.WithError(err).Error("unable to update aws_role metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": credID, "role_arn": roleARN})
}

// awsProvisioner builds the IAM provisioner from config, or (nil, nil) when
// provisioning isn't configured.
func (s *Service) awsProvisioner() (*awsiam.Provisioner, error) {
	if s.config == nil || s.config.AWS == nil {
		return nil, nil
	}
	return awsiam.NewProvisioner(s.config.AWS.Provisioning)
}

// cleanupAWSRoleIdentity deletes the dedicated IAM user backing an aws_role
// credential, when provisioning is configured and the credential carries one.
// Best-effort: logs and returns on any problem so credential deletion proceeds.
func (s *Service) cleanupAWSRoleIdentity(c *gin.Context, credID string) {
	cred, err := s.persistence.GetCredentialByID(credID)
	if err != nil || cred == nil || cred.ProviderSlug != "aws_role" || cred.Metadata == nil {
		return
	}
	var meta struct {
		IAMUserName string `json:"iam_user_name"`
	}
	if err := json.Unmarshal(*cred.Metadata, &meta); err != nil || meta.IAMUserName == "" {
		return
	}
	prov, err := s.awsProvisioner()
	if err != nil || prov == nil {
		return
	}
	if err := prov.DeleteCredentialIdentity(c.Request.Context(), meta.IAMUserName); err != nil {
		log.WithError(err).WithField("iam_user", meta.IAMUserName).Warn("failed to delete AWS role IAM user; may be orphaned")
	}
}

// generateCredUserName mints a unique IAM user name for a credential's dedicated
// Flomation identity.
func generateCredUserName() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "flomation-cred-" + hex.EncodeToString(b)
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
