// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// local.go implements the directory blob backend for single-node all-in-one
// deployments (ADR-0006). Objects are regular files under a root directory;
// downloads are served by the platform itself over short-lived HMAC-signed
// URLs, so no object-store infrastructure or credentials are needed.
//
// Writes go to a hidden temporary file in the same directory followed by
// fsync and an atomic rename, so a crash never leaves a partially written
// object readable. Content type is recorded in a sibling metadata file and
// falls back to http.DetectContentType when metadata is absent.

package blob

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// localMetaSuffix stores the content type next to an object.
const localMetaSuffix = ".meta.json"

// signedObjectPath is the gateway route that serves local objects.
const signedObjectPath = "/v1/blobs/get"

// LocalConfig configures the directory backend.
type LocalConfig struct {
	// Directory is the object root.
	Directory string
	// SigningKey authenticates download URLs; it is the platform JWT secret.
	SigningKey string
	// SignTTL bounds download URL lifetime.
	SignTTL time.Duration
}

// localStore is the directory backend.
type localStore struct {
	root    string
	signKey []byte
	signTTL time.Duration
}

// NewLocal constructs the directory backend and ensures root exists.
func NewLocal(_ context.Context, cfg LocalConfig) (*localStore, error) {
	if cfg.Directory == "" {
		return nil, errors.New("local blob directory is empty")
	}
	if cfg.SigningKey == "" {
		return nil, errors.New("local blob signing key is empty")
	}
	signTTL := cfg.SignTTL
	if signTTL <= 0 {
		signTTL = defaultPresignTTL
	}
	if err := os.MkdirAll(cfg.Directory, 0o750); err != nil {
		return nil, fmt.Errorf("create local blob directory: %w", err)
	}
	// Resolve root to an absolute, cleaned path. objectPath joins root
	// with cleaned object keys; storing a relative "./x" root would make
	// the containment prefix check fail once Join strips the leading
	// "./", rejecting every valid key.
	root, err := filepath.Abs(cfg.Directory)
	if err != nil {
		return nil, fmt.Errorf("resolve local blob directory: %w", err)
	}
	return &localStore{
		root:    root,
		signKey: []byte(cfg.SigningKey),
		signTTL: signTTL,
	}, nil
}

// objectPath maps an object key to a filesystem path inside root. The
// shared safeKey check plus filepath.Clean reject traversal in the key;
// this second containment check guarantees the resolved path stays inside
// root on every platform.
func (s *localStore) objectPath(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	path := filepath.Join(s.root, rel)
	if !isWithinDirectory(path, s.root) {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	return path, nil
}

// isWithinDirectory reports whether path is root itself or located under it.
func isWithinDirectory(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

// PutObject streams one object to a temporary file, fsyncs it and renames
// it atomically onto the target path.
func (s *localStore) PutObject(ctx context.Context, key string, r io.Reader, _ int64, contentType string) (int64, error) {
	if err := safeKey(key); err != nil {
		return 0, err
	}
	if strings.HasSuffix(key, localMetaSuffix) {
		return 0, fmt.Errorf("invalid object key %q: reserved suffix", key)
	}
	target, err := s.objectPath(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, fmt.Errorf("create object directory: %w", err)
	}

	// Hide partial writes under a dot-prefixed temp name in the same
	// directory; rename is only atomic within one filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(target), ".blob-*")
	if err != nil {
		return 0, fmt.Errorf("create temp object: %w", err)
	}
	tmpName := tmp.Name()
	n, copyErr := io.Copy(tmp, r)
	if syncErr := tmp.Sync(); syncErr != nil && copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := tmp.Close(); copyErr == nil && closeErr != nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(tmpName)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("write blob %s: %w", key, copyErr)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("finalize blob %s: %w", key, err)
	}

	if contentType != "" {
		meta := target + localMetaSuffix
		data, err := json.Marshal(localMeta{ContentType: contentType})
		if err == nil {
			if err := os.WriteFile(meta, data, 0o600); err != nil {
				return 0, fmt.Errorf("write blob metadata %s: %w", key, err)
			}
		}
	}
	return n, nil
}

// localMeta is the sibling metadata payload.
type localMeta struct {
	// ContentType is the upload-declared media type.
	ContentType string `json:"content_type"`
}

// OpenObject returns the seekable object, its info and content type for
// serving HTTP downloads with Range support. The caller closes the file.
func (s *localStore) OpenObject(key string) (*os.File, os.FileInfo, string, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return nil, nil, "", err
	}
	// The path is constructed from a key that passed safeKey and the
	// objectPath containment check, so opening it cannot read outside the
	// blob root.
	file, err := os.Open(path) // #nosec G304 -- validated object path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, "", ErrObjectNotFound
		}
		return nil, nil, "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, "", err
	}
	contentType := s.readContentType(path)
	return file, info, contentType, nil
}

// readContentType loads the declared type or detects it from the head.
func (s *localStore) readContentType(path string) string {
	// Sibling metadata and the object itself are addressed via the path
	// produced by the validated key mapping; both reads stay inside root.
	if data, err := os.ReadFile(path + localMetaSuffix); err == nil { // #nosec G304 -- validated object path
		var meta localMeta
		if json.Unmarshal(data, &meta) == nil && meta.ContentType != "" {
			return meta.ContentType
		}
	}
	f, err := os.Open(path) // #nosec G304 -- validated object path
	if err != nil {
		return "application/octet-stream"
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if ct := http.DetectContentType(head[:n]); ct != "application/octet-stream" {
		return ct
	}
	return "application/octet-stream"
}

// GetObject streams the whole object; the caller closes the reader.
func (s *localStore) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	file, _, _, err := s.OpenObject(key)
	return file, err
}

// ErrObjectNotFound reports a missing local object.
var ErrObjectNotFound = errors.New("blob object not found")

// GetObjectRange streams an inclusive byte window, translating the
// caller's length semantics exactly like the S3 backend: non-positive
// length means "from offset to end".
func (s *localStore) GetObjectRange(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	file, info, _, err := s.OpenObject(key)
	if err != nil {
		return nil, err
	}
	if offset < 0 || offset > info.Size() {
		_ = file.Close()
		return nil, fmt.Errorf("range offset out of bounds for %s", key)
	}
	remaining := info.Size() - offset
	if length <= 0 || length > remaining {
		length = remaining
	}
	return &rangedFile{
		SectionReader: io.NewSectionReader(file, offset, length),
		file:          file,
	}, nil
}

// rangedFile closes the underlying file once the section is consumed.
type rangedFile struct {
	*io.SectionReader
	file io.Closer
}

// Close releases the file handle.
func (r *rangedFile) Close() error { return r.file.Close() }

// OpenReadSeeker returns the object for HTTP serving: seekable file,
// content type and modification time.
func (s *localStore) OpenReadSeeker(_ context.Context, key string,
) (io.ReadSeekCloser, string, time.Time, error) {
	file, info, contentType, err := s.OpenObject(key)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return file, contentType, info.ModTime(), nil
}

// StatObject returns object size and an ETag derived from modification time.
func (s *localStore) StatObject(_ context.Context, key string) (int64, string, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return 0, "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, "", ErrObjectNotFound
		}
		return 0, "", err
	}
	return info.Size(), strconv.FormatInt(info.ModTime().UnixNano(), 16), nil
}

// ObjectMeta returns the object's byte size and declared/detected content
// type. The console sign endpoint returns these so the UI can render an
// image, video or text preview without a second request.
func (s *localStore) ObjectMeta(ctx context.Context, key string) (int64, string, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return 0, "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, "", ErrObjectNotFound
		}
		return 0, "", err
	}
	return info.Size(), s.readContentType(path), nil
}

// DeleteObject removes the object and its metadata. Missing objects are
// treated as success to match idempotent S3 delete semantics.
func (s *localStore) DeleteObject(_ context.Context, key string) error {
	path, err := s.objectPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete blob %s: %w", key, err)
	}
	if err := os.Remove(path + localMetaSuffix); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete blob metadata %s: %w", key, err)
	}
	return nil
}

// PresignGet builds a short-lived HMAC-signed URL pointing at the platform's
// own download route. The signature covers key and expiry, so a URL cannot
// be replayed past its lifetime or altered to name another object.
func (s *localStore) PresignGet(_ context.Context, key string) (string, error) {
	if err := safeKey(key); err != nil {
		return "", err
	}
	exp := time.Now().Add(s.signTTL).Unix()
	sig := objectSignature(s.signKey, key, exp)
	q := url.Values{}
	q.Set("key", key)
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("sig", sig)
	return signedObjectPath + "?" + q.Encode(), nil
}

// Bucket reports the backend bucket label, used in diagnostics.
func (s *localStore) Bucket() string { return "local" }

// objectSignature returns the hex HMAC-SHA256 over "key:exp".
func objectSignature(secret []byte, key string, exp int64) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = fmt.Fprintf(mac, "%s:%d", key, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyObjectURL authenticates a signed download request: the expiry must
// be in the future and the signature must match.
func VerifyObjectURL(secret, key string, exp int64, sig string) bool {
	if time.Now().Unix() >= exp {
		return false
	}
	expected := objectSignature([]byte(secret), key, exp)
	return hmac.Equal([]byte(expected), []byte(sig))
}
