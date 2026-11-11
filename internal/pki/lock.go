// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// lock.go serializes PKI material mutations across processes sharing one
// PKI directory. In the clustered Helm deployment several app pods mount the
// same RWX volume and run the "pki ensure"/"pki server" init containers at
// the same time; without mutual exclusion two pods can both observe the CA
// missing and clobber each other's writes. The lock is an advisory flock on
// a file inside the PKI directory; Linux fcntl/flock locks are honoured by
// NFSv4 (the shared filesystem backing the RWX volume).

package pki

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFileName is the advisory lock file created inside the PKI directory.
const lockFileName = ".pki.lock"

// withDirLock runs fn while holding an exclusive advisory lock on the PKI
// directory. The returned unlock function closes (and releases) the lock;
// callers must invoke it, usually via defer, even when fn failed.
func withDirLock(dir string, fn func() error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create pki directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockFileName), //nolint:gosec // G304: path is operator-controlled PKI directory
		os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open pki lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire pki lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return fn()
}
