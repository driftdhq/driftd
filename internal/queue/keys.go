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

	keyQueue                    = "driftd:queue:workitems"
	keyStackScanPrefix          = "driftd:stack_scan:"
	keyStackScanInflight        = "driftd:stack_scan:inflight:"
	keyStackScanPending         = "driftd:stack_scan:pending"
	keyLockPrefix               = "driftd:lock:project:"
	keyCloneLockPrefix          = "driftd:lock:clone:"
	keyProjectStackScans        = "driftd:stack_scans:project:"
	keyProjectStackScansOrdered = "driftd:stack_scans:project:ordered:"
	keyRunningStackScans        = "driftd:stack_scans:running"
	keyScanPrefix               = "driftd:scan:"
	keyScanRepo                 = "driftd:scan:project:"
	keyScanStackScans           = "driftd:scan:stack_scans:"
	keyScanLast                 = "driftd:scan:last:"
	keyRunningScans             = "driftd:scan:running"

	stackScanRetention = 7 * 24 * time.Hour // 7 days
	scanRetention      = 7 * 24 * time.Hour // 7 days
)

var (
	ErrProjectLocked     = errors.New("repository scan already in progress")
	ErrStackScanNotFound = errors.New("stack scan not found")
	ErrStackScanInflight = errors.New("stack scan already inflight")
)
