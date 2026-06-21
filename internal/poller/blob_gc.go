package poller

// Blob GC poller — sweeps blob_object rows whose expires_at has
// passed. TTLs are short for tool_output blobs (1 h) and long for
// inbound attachments (30 d), so an hourly sweep is the right cadence
// — frequent enough that tool outputs don't pile up, slow enough that
// the delete pressure on a healthy install is invisible.
//
// The blob_quota_daily counter table is pruned in the same tick so
// it doesn't grow unbounded; rows older than 7 days are useless.

import (
	"time"

	log "github.com/sirupsen/logrus"
)

// BlobGCPersistence is the slice of the persistence API the blob GC
// poller touches. Kept narrow to keep tests trivial.
type BlobGCPersistence interface {
	SweepExpiredBlobs(limit int) (int64, error)
	SweepStaleBlobQuotaRows() (int64, error)
}

// BlobGCPoller deletes expired blob rows + stale daily-quota rows.
type BlobGCPoller struct {
	persistence BlobGCPersistence
}

const (
	blobGCInterval  = 1 * time.Hour
	blobGCBatchSize = 500
)

// StartBlobGCPoller spins up the sweeper goroutine.
func StartBlobGCPoller(p BlobGCPersistence) *BlobGCPoller {
	bp := &BlobGCPoller{persistence: p}
	go bp.watch()
	return bp
}

func (bp *BlobGCPoller) watch() {
	// Stagger relative to other 1h pollers so they don't all hit the
	// DB at the same minute on cold start.
	time.Sleep(45 * time.Second)

	ticker := time.NewTicker(blobGCInterval)
	defer ticker.Stop()

	log.Info("blob GC poller started (API-side, 1h interval)")
	bp.poll()

	for range ticker.C {
		bp.poll()
	}
}

// poll runs one sweep. Loops on expired-blob deletion until the
// batch returns < batchSize rows, so a backlog drains in a single
// tick rather than waiting another hour per batch.
func (bp *BlobGCPoller) poll() {
	totalBlobs := int64(0)
	for {
		n, err := bp.persistence.SweepExpiredBlobs(blobGCBatchSize)
		if err != nil {
			log.WithError(err).Warn("blob GC: sweep failed")
			break
		}
		totalBlobs += n
		if n < int64(blobGCBatchSize) {
			break
		}
	}

	quotaRows, err := bp.persistence.SweepStaleBlobQuotaRows()
	if err != nil {
		log.WithError(err).Warn("blob GC: quota sweep failed")
	}

	if totalBlobs > 0 || quotaRows > 0 {
		log.WithFields(log.Fields{
			"blobs_deleted":      totalBlobs,
			"quota_rows_deleted": quotaRows,
		}).Info("blob GC: swept")
	}
}
