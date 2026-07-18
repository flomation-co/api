package poller

import (
	"context"
	"time"

	"flomation.app/automate/api/internal/awsiam"
	apiconfig "flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

const (
	// awsRoleCleanupInterval is how often the sweep runs.
	awsRoleCleanupInterval = 30 * time.Minute
	// awsRoleCleanupAge is how old an incomplete credential must be before it's
	// swept. Generous on purpose: a real user may sit in wizard step 2 building
	// their AWS role for a while, and deleting their identity mid-setup is far
	// worse than an orphan lingering (the minted user is assume-role-only and
	// trusted by no role, so it can do nothing).
	awsRoleCleanupAge = 6 * time.Hour
)

// AWSRoleCleanupPoller removes aws_role credentials (and their dedicated IAM
// users) whose wizard was never completed.
type AWSRoleCleanupPoller struct {
	p    *persistence.Service
	prov *awsiam.Provisioner
}

// StartAWSRoleCleanupPoller starts the sweep, or no-ops when AWS provisioning
// isn't configured (there'd be no per-credential IAM users to clean up).
func StartAWSRoleCleanupPoller(p *persistence.Service, awsCfg *apiconfig.AWSConfig) *AWSRoleCleanupPoller {
	if awsCfg == nil || awsCfg.Provisioning == nil {
		return nil
	}
	prov, err := awsiam.NewProvisioner(awsCfg.Provisioning)
	if err != nil || prov == nil {
		log.WithError(err).Warn("aws role cleanup poller: provisioner unavailable, not starting")
		return nil
	}
	cp := &AWSRoleCleanupPoller{p: p, prov: prov}
	go cp.watch()
	return cp
}

func (cp *AWSRoleCleanupPoller) watch() {
	time.Sleep(30 * time.Second) // settle after startup
	ticker := time.NewTicker(awsRoleCleanupInterval)
	defer ticker.Stop()
	log.Info("API-side AWS role cleanup poller started")
	cp.sweep()
	for range ticker.C {
		cp.sweep()
	}
}

func (cp *AWSRoleCleanupPoller) sweep() {
	targets, err := cp.p.ListIncompleteAWSRoleCredentials(int(awsRoleCleanupAge.Seconds()))
	if err != nil {
		log.WithError(err).Warn("aws role cleanup poller: failed to list incomplete credentials")
		return
	}
	if len(targets) == 0 {
		return
	}
	ctx := context.Background()
	removed := 0
	for _, t := range targets {
		if t.IAMUserName != "" {
			if err := cp.prov.DeleteCredentialIdentity(ctx, t.IAMUserName); err != nil {
				log.WithError(err).WithField("iam_user", t.IAMUserName).Warn("aws role cleanup: failed to delete IAM user")
			}
		}
		if err := cp.p.DeleteCredential(t.ID, t.EnvironmentID); err != nil {
			log.WithError(err).WithField("credential", t.ID).Warn("aws role cleanup: failed to delete credential row")
			continue
		}
		removed++
	}
	log.WithField("removed", removed).Info("aws role cleanup poller: swept abandoned wizard credentials")
}
