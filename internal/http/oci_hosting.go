package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	ocicommon "github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"
	log "github.com/sirupsen/logrus"
)

// OCI Resource Manager only fetches stack zips from supported providers (Object
// Storage / GitHub / GitLab) — a self-served URL is rejected with
// InvalidParameter(400) before RM even requests it. So the connector uploads each
// "Connect Oracle Cloud" provisioning stack to Flomation's own Object Storage
// bucket and hands RM a pre-authenticated request (PAR) URL.

// ociHostConfigured reports whether stack hosting is set up on this server.
func (s *Service) ociHostConfigured() bool {
	h := s.ociHostCfg()
	return h != nil && strings.TrimSpace(h.Bucket) != "" && strings.TrimSpace(h.Tenancy) != "" && strings.TrimSpace(h.PrivateKey) != ""
}

func (s *Service) ociHostCfg() *ociHostCfg {
	if s.config == nil || s.config.OCIHosting == nil {
		return nil
	}
	h := s.config.OCIHosting
	return &ociHostCfg{
		Tenancy: h.Tenancy, User: h.User, Region: h.Region, Fingerprint: h.Fingerprint,
		PrivateKey: h.PrivateKey, Passphrase: h.Passphrase, Bucket: h.Bucket, Namespace: h.Namespace,
	}
}

// ociHostCfg mirrors config.OCIHostingConfig so this package needn't import it.
type ociHostCfg struct {
	Tenancy, User, Region, Fingerprint, PrivateKey, Passphrase, Bucket, Namespace string
}

func (s *Service) ociHostClient(h *ociHostCfg) (objectstorage.ObjectStorageClient, string, error) {
	var pass *string
	if h.Passphrase != "" {
		pass = &h.Passphrase
	}
	provider := ocicommon.NewRawConfigurationProvider(h.Tenancy, h.User, h.Region, h.Fingerprint, h.PrivateKey, pass)
	client, err := objectstorage.NewObjectStorageClientWithConfigurationProvider(provider)
	if err != nil {
		return objectstorage.ObjectStorageClient{}, "", err
	}
	namespace := h.Namespace
	if namespace == "" {
		ns, err := client.GetNamespace(context.Background(), objectstorage.GetNamespaceRequest{})
		if err != nil {
			return objectstorage.ObjectStorageClient{}, "", fmt.Errorf("resolve namespace: %w", err)
		}
		namespace = *ns.Value
	}
	return client, namespace, nil
}

// hostStackZip uploads the stack zip to the hosting bucket and returns a
// public, no-auth PAR URL that OCI Resource Manager can fetch. The PAR is
// object-read only and expires in 7 days (the setup window); after that the
// operator re-creates the connection.
func (s *Service) hostStackZip(ctx context.Context, zipBytes []byte, objectName string) (string, error) {
	h := s.ociHostCfg()
	if h == nil {
		return "", fmt.Errorf("OCI stack hosting is not configured")
	}
	client, namespace, err := s.ociHostClient(h)
	if err != nil {
		return "", err
	}

	// Ensure the bucket exists — best-effort. The dedicated hosting user is scoped
	// to objects in an already-provisioned bucket and intentionally CANNOT create
	// buckets, so ignore any error here and let PutObject be the real gate (it
	// fails clearly if the bucket is genuinely missing).
	if _, err = client.CreateBucket(ctx, objectstorage.CreateBucketRequest{
		NamespaceName:       &namespace,
		CreateBucketDetails: objectstorage.CreateBucketDetails{Name: &h.Bucket, CompartmentId: &h.Tenancy},
	}); err != nil && !strings.Contains(err.Error(), "BucketAlreadyExists") && !strings.Contains(err.Error(), "already own") {
		log.WithError(err).Debug("create hosting bucket (expected when the scoped user lacks bucket-manage)")
	}

	cl := int64(len(zipBytes))
	if _, err := client.PutObject(ctx, objectstorage.PutObjectRequest{
		NamespaceName: &namespace, BucketName: &h.Bucket, ObjectName: &objectName,
		ContentLength: &cl, PutObjectBody: io.NopCloser(bytes.NewReader(zipBytes)),
	}); err != nil {
		return "", fmt.Errorf("upload stack: %w", err)
	}

	parName := "flomation-connect-" + objectName
	expires := &ocicommon.SDKTime{Time: time.Now().Add(7 * 24 * time.Hour)}
	par, err := client.CreatePreauthenticatedRequest(ctx, objectstorage.CreatePreauthenticatedRequestRequest{
		NamespaceName: &namespace, BucketName: &h.Bucket,
		CreatePreauthenticatedRequestDetails: objectstorage.CreatePreauthenticatedRequestDetails{
			Name: &parName, ObjectName: &objectName,
			AccessType:  objectstorage.CreatePreauthenticatedRequestDetailsAccessTypeObjectread,
			TimeExpires: expires,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create PAR: %w", err)
	}
	return fmt.Sprintf("https://objectstorage.%s.oraclecloud.com%s", h.Region, *par.AccessUri), nil
}

// deleteHostedStack best-effort removes a credential's hosted stack object on
// credential deletion. A leftover object is harmless (small, PAR expires), so a
// failure only logs.
func (s *Service) deleteHostedStack(objectName string) {
	if objectName == "" || !s.ociHostConfigured() {
		return
	}
	h := s.ociHostCfg()
	client, namespace, err := s.ociHostClient(h)
	if err != nil {
		log.WithError(err).Warn("oci host client for stack cleanup")
		return
	}
	if _, err := client.DeleteObject(context.Background(), objectstorage.DeleteObjectRequest{
		NamespaceName: &namespace, BucketName: &h.Bucket, ObjectName: &objectName,
	}); err != nil {
		log.WithError(err).WithField("object", objectName).Warn("failed to delete hosted OCI stack; harmless")
	}
}

// cleanupOCIStack removes the hosted provisioning stack for an oci_key credential
// when it's deleted. Best-effort — a leftover object is harmless.
func (s *Service) cleanupOCIStack(credID string) {
	cred, err := s.persistence.GetCredentialByID(credID)
	if err != nil || cred == nil || cred.ProviderSlug != "oci_key" || cred.Metadata == nil {
		return
	}
	var meta struct {
		StackObject string `json:"stack_object"`
	}
	if err := json.Unmarshal(*cred.Metadata, &meta); err != nil || meta.StackObject == "" {
		return
	}
	s.deleteHostedStack(meta.StackObject)
}
