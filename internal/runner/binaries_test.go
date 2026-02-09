package runner

import "testing"

func TestVersionLockReturnsSameInstance(t *testing.T) {
	lockA := versionLock(&tfInstallLocks, "1.2.3")
	lockB := versionLock(&tfInstallLocks, "1.2.3")
	if lockA != lockB {
		t.Fatal("expected same lock for same version")
	}

	lockC := versionLock(&tfInstallLocks, "2.0.0")
	if lockA == lockC {
		t.Fatal("expected different lock for different version")
	}
}
