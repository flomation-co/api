package http

import (
	"archive/zip"
	"bytes"
	"crypto/md5" // #nosec G501 -- an OCI API-key fingerprint is DEFINED as the MD5 of the DER public key; not used as a security primitive
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	ocicommon "github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
	log "github.com/sirupsen/logrus"

	api "flomation.app/automate/api"
)

// Managed OCI signing-key connector.
//
// Unlike aws_role (which mints an identity in FLOMATION's account), an oci_key
// credential is provisioned in the CUSTOMER's tenancy by a one-click Resource
// Manager stack. The flow:
//
//  1. createOCIKeyCredential — Flomation generates an RSA keypair, stores the
//     private key encrypted + the public key/fingerprint/tenancy/region in
//     metadata (status 'pending'), and returns a "Deploy to Oracle Cloud" URL.
//  2. The customer applies the stack in their console — it creates a dedicated
//     user + group + (compartment- or tenancy-scoped) policy and uploads
//     Flomation's public key as that user's API key. Its outputs are the new
//     user OCID and the chosen compartment.
//  3. setOCIConnection — captures the user OCID (+ compartment) and flips the
//     credential to 'active'.
//  4. testOCIAccess — signs a cheap OCI call with the stored key to confirm.
//
// A signing key is the universal OCI auth (every service accepts it identically),
// so a connected credential is never conditional on which service a flow calls.

// createOCIKeyCredential generates the managed keypair and returns the one-click
// provisioning stack. The private key never leaves Flomation; the customer never
// sees or pastes a key.
func (s *Service) createOCIKeyCredential(c *gin.Context, environmentID string, env *api.Environment, req createCredentialRequest) {
	if !s.ociHostConfigured() {
		// No managed stack hosting on this server → fall back to manual entry: the
		// operator pastes an existing OCI API signing key, which we store and
		// activate directly. Never a dead-end 503 at the finish line.
		s.createOCIKeyManual(c, environmentID, env, req)
		return
	}
	tenancy := strings.TrimSpace(req.TenancyOCID)
	region := strings.TrimSpace(req.Region)
	if !validOCID(tenancy, "tenancy") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid tenancy OCID is required (ocid1.tenancy.oc1..…)"})
		return
	}
	if region == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a home region is required (e.g. uk-london-1)"})
		return
	}
	scope := strings.TrimSpace(req.Scope)
	if scope != "tenancy" {
		scope = "compartment"
	}

	privatePEM, publicPEM, fingerprint, err := generateOCIKeyPair()
	if err != nil {
		log.WithError(err).Error("unable to generate OCI keypair")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	stackToken := generateStackToken()

	// Render the stack and publish it to Object Storage BEFORE inserting the row,
	// so a hosting failure never orphans a credential. RM only fetches stack zips
	// from supported providers, so the deploy URL wraps a PAR to our bucket.
	zipBytes, err := renderOCIStackZip(publicPEM, scope, stackToken)
	if err != nil {
		log.WithError(err).Error("unable to render OCI stack")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	objectName := "stacks/" + stackToken + ".zip"
	parURL, err := s.hostStackZip(c.Request.Context(), zipBytes, objectName)
	if err != nil {
		log.WithError(err).Error("unable to host OCI stack")
		c.JSON(http.StatusBadGateway, gin.H{"error": "couldn't publish the provisioning stack — check the server's Oracle Cloud hosting configuration"})
		return
	}

	metaBytes, err := json.Marshal(map[string]interface{}{
		"tenancy_ocid":     tenancy,
		"region":           region,
		"fingerprint":      fingerprint,
		"public_key":       publicPEM,
		"scope":            scope,
		"stack_token":      stackToken,
		"stack_object":     objectName, // for cleanup on delete
		"user_ocid":        "",         // captured after the stack applies
		"compartment_ocid": "",
	})
	if err != nil {
		log.WithError(err).Error("unable to encode oci_key metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	credID, err := s.persistence.CreateOCIKeyCredential(environmentID, req.Name, env.SecretKey, privatePEM, json.RawMessage(metaBytes))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			c.JSON(http.StatusConflict, gin.H{"error": "credential name already exists in this environment"})
			return
		}
		log.WithError(err).Error("unable to create oci_key credential")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":          credID,
		"fingerprint": fingerprint,
		"public_key":  publicPEM,
		"deploy_url":  ociDeployURL(parURL),
		"status":      "pending",
	})
}

// createOCIKeyManual creates an oci_key credential from an operator-supplied OCI API
// signing key. It is the fallback for servers with NO managed stack hosting: rather
// than dead-ending the wizard with a 503, the operator pastes an existing key (the
// tenancy/user OCID, region, fingerprint and unencrypted private-key PEM they get
// from the OCI console) and we store + activate it directly — no keypair generation,
// no provisioning stack, no shepherding. Once stored it behaves exactly like a
// managed credential (same metadata shape, same testOCIAccess path).
func (s *Service) createOCIKeyManual(c *gin.Context, environmentID string, env *api.Environment, req createCredentialRequest) {
	tenancy := strings.TrimSpace(req.TenancyOCID)
	userOCID := strings.TrimSpace(req.UserOCID)
	region := strings.TrimSpace(req.Region)
	fingerprint := strings.TrimSpace(req.Fingerprint)
	privateKey := req.PrivateKey

	if !validOCID(tenancy, "tenancy") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid tenancy OCID is required (ocid1.tenancy.oc1..…)"})
		return
	}
	if !validOCID(userOCID, "user") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid user OCID is required (ocid1.user.oc1..…)"})
		return
	}
	if region == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a home region is required (e.g. uk-london-1)"})
		return
	}
	if fingerprint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the API key fingerprint is required"})
		return
	}
	if strings.TrimSpace(privateKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the private key (PEM) is required"})
		return
	}
	// Parse the key up front so a bad or encrypted PEM fails cleanly at Create,
	// not at runtime. NewRawConfigurationProvider + PrivateRSAKey validates it.
	provider := ocicommon.NewRawConfigurationProvider(tenancy, userOCID, region, fingerprint, privateKey, nil)
	if _, err := provider.PrivateRSAKey(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the private key couldn't be read — paste the full unencrypted PEM, including the BEGIN and END lines"})
		return
	}

	metaBytes, err := json.Marshal(map[string]interface{}{
		"tenancy_ocid": tenancy,
		"user_ocid":    userOCID,
		"region":       region,
		"fingerprint":  fingerprint,
		"manual":       true,
	})
	if err != nil {
		log.WithError(err).Error("unable to encode oci_key metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	credID, err := s.persistence.CreateOCIKeyCredential(environmentID, req.Name, env.SecretKey, privateKey, json.RawMessage(metaBytes))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			c.JSON(http.StatusConflict, gin.H{"error": "credential name already exists in this environment"})
			return
		}
		log.WithError(err).Error("unable to create manual oci_key credential")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	// A manually-entered key is usable immediately — there is no stack to apply.
	if err := s.persistence.ActivateCredential(credID); err != nil {
		log.WithError(err).Error("unable to activate manual oci_key credential")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": credID, "fingerprint": fingerprint, "status": "active"})
}

// setOCIConnection is step 3: capture the user OCID (and the compartment the stack
// scoped to) that the customer copies from the applied stack's outputs, then
// activate the credential.
func (s *Service) setOCIConnection(c *gin.Context) {
	environmentID := c.Param("environment")
	credID := c.Param("id")

	// Authorize: the caller must own the environment in the path, and the credential
	// must belong to it — otherwise an authenticated user could activate or patch
	// another tenant's credential by guessing its id (BOLA). jwtMiddleware only
	// authenticates; it does not scope the object.
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}
	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "environment not found"})
		return
	}

	var body struct {
		UserOCID        string `json:"user_ocid"`
		CompartmentOCID string `json:"compartment_ocid"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	userOCID := strings.TrimSpace(body.UserOCID)
	if !validOCID(userOCID, "user") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a valid user OCID is required (ocid1.user.oc1..…)"})
		return
	}
	compartment := strings.TrimSpace(body.CompartmentOCID)
	if compartment != "" && !validOCID(compartment, "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "compartment OCID is not a valid OCID"})
		return
	}

	cred, err := s.persistence.GetCredentialByID(credID)
	if err != nil || cred == nil || cred.ProviderSlug != "oci_key" || cred.EnvironmentID != environmentID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Oracle Cloud credential not found"})
		return
	}

	patch := map[string]interface{}{"user_ocid": userOCID}
	if compartment != "" {
		patch["compartment_ocid"] = compartment
	}
	merged, err := api.MergeMetadata(cred.Metadata, patch)
	if err != nil {
		log.WithError(err).Error("unable to merge oci_key metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := s.persistence.UpdateCredentialMetadata(credID, merged); err != nil {
		log.WithError(err).Error("unable to update oci_key metadata")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := s.persistence.ActivateCredential(credID); err != nil {
		log.WithError(err).Error("unable to activate oci_key credential")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	// The provisioning stack has done its job now the connection is live — delete
	// it so hosted objects don't accumulate. Best-effort.
	s.cleanupOCIStack(credID)

	c.JSON(http.StatusOK, gin.H{"id": credID, "user_ocid": userOCID, "status": "active"})
}

// testOCIAccess signs a cheap, always-permitted OCI call (list region
// subscriptions) with the stored key, so the wizard can confirm the connection
// works before finishing. Returns {ok:true} or {ok:false, error:<reason>}.
func (s *Service) testOCIAccess(c *gin.Context) {
	environmentID := c.Param("environment")
	credID := c.Param("id")
	user := s.getUserFromContext(c)

	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}
	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil || env == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "environment not found"})
		return
	}

	privateKey, metaRaw, err := s.persistence.GetCredentialWithMetaByID(credID, env.SecretKey)
	if err != nil || privateKey == nil || metaRaw == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Oracle Cloud credential not found"})
		return
	}
	var meta struct {
		TenancyID   string `json:"tenancy_ocid"`
		UserID      string `json:"user_ocid"`
		Region      string `json:"region"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(*metaRaw, &meta); err != nil {
		log.WithError(err).Error("unable to read oci_key credential metadata")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the stored connection details couldn't be read"})
		return
	}
	if meta.UserID == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "connect the stack first — the user OCID hasn't been captured yet"})
		return
	}

	provider := ocicommon.NewRawConfigurationProvider(meta.TenancyID, meta.UserID, meta.Region, meta.Fingerprint, *privateKey, nil)
	client, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": ociReason(err)})
		return
	}
	if _, err := client.ListRegionSubscriptions(c.Request.Context(), identity.ListRegionSubscriptionsRequest{TenancyId: &meta.TenancyID}); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": ociReason(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- helpers ----

// generateOCIKeyPair mints a 2048-bit RSA keypair and returns the PKCS#8 private
// key PEM, the PKIX public key PEM, and the OCI fingerprint (MD5 of the DER public
// key, colon-separated hex).
func generateOCIKeyPair() (privatePEM, publicPEM, fingerprint string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", "", err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", "", err
	}
	privatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}))

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", "", err
	}
	publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	fingerprint = ociKeyFingerprint(pubDER)
	return privatePEM, publicPEM, fingerprint, nil
}

// ociKeyFingerprint computes the OCI API-key fingerprint: the MD5 digest of the
// DER-encoded (PKIX) public key, rendered as colon-separated lowercase hex pairs.
func ociKeyFingerprint(pubDER []byte) string {
	sum := md5.Sum(pubDER) // #nosec G401 -- the OCI fingerprint spec mandates MD5; not a security primitive
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}

// ociDeployURL wraps a stack zip URL in OCI's native "Deploy to Oracle Cloud"
// Resource Manager create-stack link.
func ociDeployURL(zipURL string) string {
	return "https://cloud.oracle.com/resourcemanager/stacks/create?zipUrl=" + url.QueryEscape(zipURL)
}

// generateStackToken mints an unguessable token that gates the (unauthenticated)
// stack download.
func generateStackToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// validOCID does a lightweight shape check on an OCID. When kind is non-empty it
// also checks the resource-type segment (e.g. "tenancy", "user").
func validOCID(ocid, kind string) bool {
	if !strings.HasPrefix(ocid, "ocid1.") {
		return false
	}
	if kind != "" && !strings.HasPrefix(ocid, "ocid1."+kind+".") {
		return false
	}
	// Real OCIDs are ocid1.<type>.<realm>.[region].<unique> — five dot-separated
	// segments (the region segment may be empty) ending in a substantial unique id.
	// The old len>14 check let almost anything through.
	if strings.Count(ocid, ".") < 4 {
		return false
	}
	return len(ocid)-strings.LastIndexByte(ocid, '.')-1 >= 15
}

// ociReason returns the OCI service failure message without leaking key material.
func ociReason(err error) string {
	if err == nil {
		return ""
	}
	if failure, ok := ocicommon.IsServiceError(err); ok {
		return fmt.Sprintf("%s (%s)", failure.GetMessage(), failure.GetCode())
	}
	msg := err.Error()
	// Never surface a PEM in an error string.
	if strings.Contains(msg, "PRIVATE KEY") {
		return "could not sign the request with the stored key"
	}
	return msg
}

// renderOCIStackZip builds the in-memory Resource Manager stack — a single
// main.tf that creates a dedicated user + group + scoped policy and uploads
// Flomation's public key. Resource Manager auto-injects tenancy_ocid, region and
// compartment_ocid (rendering compartment_ocid as a native picker), so no
// schema.yaml is needed — and shipping one risks an RM validation rejection.
// suffix makes resource names unique per credential; scope bakes the operator's
// compartment-vs-tenancy choice into the stack's default.
func renderOCIStackZip(publicKeyPEM, scope, credID string) ([]byte, error) {
	suffix := credID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	if scope != "tenancy" {
		scope = "compartment"
	}
	mainTF := fmt.Sprintf(ociStackMainTF, suffix, indentPEM(publicKeyPEM), scope)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("main.tf")
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(mainTF)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// indentPEM indents a PEM block for embedding inside a Terraform heredoc.
func indentPEM(pemStr string) string {
	lines := strings.Split(strings.TrimRight(pemStr, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// ociStackMainTF: %s = resource-name suffix, %s = indented public key PEM.
const ociStackMainTF = `terraform {
  required_providers {
    oci = { source = "oracle/oci" }
  }
}

# tenancy_ocid, region and compartment_ocid are injected by Resource Manager.
variable "tenancy_ocid" {}
variable "region" {}
variable "compartment_ocid" { default = "" }
variable "scope" { default = "%[3]s" }

provider "oci" {}

locals {
  is_tenancy = var.scope == "tenancy"
  statement = local.is_tenancy ? "Allow group ${oci_identity_group.flomation.name} to manage all-resources in tenancy" : "Allow group ${oci_identity_group.flomation.name} to manage all-resources in compartment id ${var.compartment_ocid}"
}

resource "oci_identity_user" "flomation" {
  compartment_id = var.tenancy_ocid
  name           = "flomation-automate-%[1]s"
  description    = "Flomation Automate — managed automation user"
  # Identity Domains tenancies require an email on user creation; harmless on
  # legacy IAM. A synthetic, unique, no-reply address (never emailed).
  email = "flomation-automate-%[1]s@noreply.flomation.co"
}

resource "oci_identity_group" "flomation" {
  compartment_id = var.tenancy_ocid
  name           = "flomation-automate-grp-%[1]s"
  description    = "Flomation Automate — managed automation group"
}

resource "oci_identity_user_group_membership" "flomation" {
  user_id  = oci_identity_user.flomation.id
  group_id = oci_identity_group.flomation.id
}

# Policies are created at the tenancy root so no compartment-level manage-policy
# grant is needed; the statement itself scopes access to the chosen compartment.
resource "oci_identity_policy" "flomation" {
  compartment_id = var.tenancy_ocid
  name           = "flomation-automate-policy-%[1]s"
  description    = "Flomation Automate — managed access"
  statements     = [local.statement]
}

resource "oci_identity_api_key" "flomation" {
  user_id   = oci_identity_user.flomation.id
  key_value = <<-EOK
%[2]s
  EOK
}

output "flomation_user_ocid" {
  value = oci_identity_user.flomation.id
}

output "flomation_compartment_ocid" {
  value = local.is_tenancy ? var.tenancy_ocid : var.compartment_ocid
}

output "flomation_fingerprint" {
  value = oci_identity_api_key.flomation.fingerprint
}
`
