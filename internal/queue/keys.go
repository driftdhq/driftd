package queue

import (
	"errors"
	"time"
)

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"

	keyQueue                 = "driftd:queue:workitems"
	keyStackScanPrefix       = "driftd:stack_scan:"
	keyStackScanInflight     = "driftd:stack_scan:inflight:"
	keyLockPrefix            = "driftd:lock:repo:"
	keyRepoStackScans        = "driftd:stack_scans:repo:"
	keyRepoStackScansOrdered = "driftd:stack_scans:repo:ordered:"
	keyRunningStackScans     = "driftd:stack_scans:running"
	keyScanPrefix            = "driftd:scan:"
	keyScanRepo              = "driftd:scan:repo:"
	keyScanStackScans        = "driftd:scan:stack_scans:"
	keyScanLast              = "driftd:scan:last:"
	keyRunningScans          = "driftd:scan:running"

	stackScanRetention = 7 * 24 * time.Hour // 7 days
	scanRetention      = 7 * 24 * time.Hour // 7 days
)

var (
	ErrRepoLocked        = errors.New("repository scan already in progress")
	ErrStackScanNotFound = errors.New("stack scan not found")
	ErrStackScanInflight = errors.New("stack scan already inflight")
)
