// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package blob stores and serves large/binary payloads off the core
// database (ADR-0006). Firmware packages, bulk import/export files and
// telemetry cold archives are written to an S3-compatible object store
// (MinIO, SeaweedFS, AWS S3, ...); PostgreSQL and NATS never carry the bulk
// bytes. The store issues short-lived presigned URLs so devices and browsers
// fetch bytes directly from the object store through a gateway/ingress rather
// proxying them through the API.
//
// The store is S3-agnostic beyond standard SigV4 path-style access; a single
// region and bucket are used. A separate, externally reachable endpoint host
// (PublicEndpoint) is used when signing download URLs so the signature host
// matches what a gateway/edge actually reaches, while uploads/list/delete use
// the internal cluster endpoint.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config configures the S3-compatible blob store.
type Config struct {
	// Endpoint is the internal host[:port] the platform uses for
	// put/list/delete (no scheme), e.g. seaweedfs-s3.ns:8333.
	Endpoint string `yaml:"endpoint"`

	// PublicEndpoint is the host[:port] embedded in presigned download
	// URLs, i.e. what gateways/edges reach. Defaults to Endpoint.
	PublicEndpoint string `yaml:"public_endpoint"`

	// Region is the SigV4 region (SeaweedFS ignores the value).
	Region string `yaml:"region"`

	// AccessKey / SecretKey are S3 credentials, normally supplied via
	// environment-expanded config, never committed.
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`

	// Bucket holds every tenant-prefixed object.
	Bucket string `yaml:"bucket"`

	// PresignTTL is the lifetime of a download URL.
	PresignTTL time.Duration `yaml:"-"`

	// PresignTTLString is the YAML form of PresignTTL (e.g. "15m").
	PresignTTLString string `yaml:"presign_ttl"`

	// Secure selects HTTPS for the internal endpoint.
	Secure bool `yaml:"secure"`
	// PublicSecure selects HTTPS for presigned download URLs; it matters
	// when PublicEndpoint differs from Endpoint (plain HTTP in-cluster,
	// TLS-terminated public ingress). Ignored when they are the same.
	PublicSecure bool `yaml:"public_secure"`
}

// Enabled reports whether object storage is configured.
func (c Config) Enabled() bool {
	return c.Endpoint != "" && c.AccessKey != "" && c.SecretKey != "" && c.Bucket != ""
}

// ErrNotConfigured is returned when the blob store is used without an S3
// configuration.
var ErrNotConfigured = errors.New("blob storage not configured")

// Store wraps an S3 client plus the public signing endpoint.
type Store struct {
	client *minio.Client
	signer *minio.Client
	bucket string
	ttl    time.Duration
}

const defaultPresignTTL = 15 * time.Minute

// New constructs a blob store and ensures the bucket exists.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if !cfg.Enabled() {
		return nil, ErrNotConfigured
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("blob s3 client: %w", err)
	}
	signer := client
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.Endpoint {
		signer, err = minio.New(cfg.PublicEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
			Secure: cfg.PublicSecure,
			Region: region,
		})
		if err != nil {
			return nil, fmt.Errorf("blob public s3 client: %w", err)
		}
	}
	ttl := cfg.PresignTTL
	if ttl <= 0 {
		if d, err := time.ParseDuration(cfg.PresignTTLString); err == nil && d > 0 {
			ttl = d
		} else {
			ttl = defaultPresignTTL
		}
	}
	s := &Store{client: client, signer: signer, bucket: cfg.Bucket, ttl: ttl}
	if err := s.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureBucket creates the configured bucket if it is missing. SeaweedFS and
// MinIO both treat a pre-existing bucket as success.
func (s *Store) ensureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("probe blob bucket: %w", err)
	}
	if !exists {
		if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("create blob bucket %q: %w", s.bucket, err)
		}
	}
	return nil
}

// PutObject uploads one object with its content type and returns the size
// stored.
func (s *Store) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	if err := safeKey(key); err != nil {
		return 0, err
	}
	info, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return 0, fmt.Errorf("put blob %s: %w", key, err)
	}
	return info.Size, nil
}

// GetObject streams one object. The caller must close the returned reader.
func (s *Store) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.getObject(ctx, key, 0, 0)
}

// GetObjectRange streams one object restricted to the inclusive byte range
// [offset, offset+length-1]. A non-positive length means "from offset to the
// end", matching an open-ended HTTP Range header. The returned object reports
// the object's full size and the satisfied range through its HTTP headers.
func (s *Store) GetObjectRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	return s.getObject(ctx, key, offset, length)
}

// getObject backs both the full and ranged readers; non-positive offset and
// length request the whole object.
func (s *Store) getObject(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := safeKey(key); err != nil {
		return nil, err
	}
	opts := minio.GetObjectOptions{}
	if offset > 0 || length > 0 {
		// minio-go's SetRange takes an INCLUSIVE end, not a byte count, and
		// "bytes=offset-" is expressed as end==0. Our API uses a length where
		// length<=0 means open-ended, so translate at this boundary.
		var end int64
		if length > 0 {
			end = offset + length - 1
		}
		if err := opts.SetRange(offset, end); err != nil {
			return nil, fmt.Errorf("set blob range %s: %w", key, err)
		}
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, opts)
	if err != nil {
		return nil, fmt.Errorf("get blob %s: %w", key, err)
	}
	return obj, nil
}

// StatObject returns the size and ETag of an object.
func (s *Store) StatObject(ctx context.Context, key string) (size int64, etag string, err error) {
	if err := safeKey(key); err != nil {
		return 0, "", err
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("stat blob %s: %w", key, err)
	}
	return info.Size, info.ETag, nil
}

// ObjectMeta returns the object's byte size and declared content type.
// Mirrors localStore.ObjectMeta so the console sign endpoint can tell
// the UI how to render the object.
func (s *Store) ObjectMeta(ctx context.Context, key string) (int64, string, error) {
	if err := safeKey(key); err != nil {
		return 0, "", err
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return 0, "", ErrObjectNotFound
		}
		return 0, "", fmt.Errorf("stat blob %s: %w", key, err)
	}
	return info.Size, info.ContentType, nil
}

// DeleteObject removes one object (idempotent).
func (s *Store) DeleteObject(ctx context.Context, key string) error {
	if err := safeKey(key); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete blob %s: %w", key, err)
	}
	return nil
}

// OpenReadSeeker returns the object as a seekable reader plus its content
// type and modification time for HTTP serving with Range. minio objects
// implement Seek through targeted range requests.
func (s *Store) OpenReadSeeker(ctx context.Context, key string,
) (io.ReadSeekCloser, string, time.Time, error) {
	if err := safeKey(key); err != nil {
		return nil, "", time.Time{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("open blob %s: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, "", time.Time{}, fmt.Errorf("stat blob %s: %w", key, err)
	}
	return obj, info.ContentType, info.LastModified, nil
}

// PresignGet returns a short-lived presigned GET URL signed against the
// public endpoint so an external gateway/edge can download the object without
// S3 credentials.
func (s *Store) PresignGet(ctx context.Context, key string) (string, error) {
	if err := safeKey(key); err != nil {
		return "", err
	}
	u, err := s.signer.PresignedGetObject(ctx, s.bucket, key, s.ttl, nil)
	if err != nil {
		return "", fmt.Errorf("presign blob %s: %w", key, err)
	}
	return u.String(), nil
}

// Bucket exposes the configured bucket name.
func (s *Store) Bucket() string { return s.bucket }

// FirmwareKey returns the canonical object key for a firmware package:
// firmware/<tenant>/<object>. The object segment is caller-provided (a task
// id or content hash) and must not contain traversal.
func FirmwareKey(tenantID, object string) string {
	return "firmware/" + strings.Trim(tenantID, "/") + "/" + strings.TrimPrefix(object, "/")
}

// MediaKeyPrefix namespaces device media objects.
const MediaKeyPrefix = "media/"

// MediaKey returns the canonical object key for one device media object:
// media/<tenant>/<device>/<object>. The tenant and device scopes are part of
// the key so ownership can be verified on every device download.
func MediaKey(tenantID, deviceID, object string) string {
	return MediaKeyPrefix + strings.Trim(tenantID, "/") + "/" +
		strings.Trim(deviceID, "/") + "/" + strings.TrimPrefix(object, "/")
}

// MediaKeyOwner returns the tenant and device encoded in a media object key
// and whether the key is a well-formed media key. Callers compare the
// result to the authenticated identity, so a device cannot read another
// device's object by guessing its key.
func MediaKeyOwner(objectKey string) (tenantID, deviceID string, ok bool) {
	parts := strings.Split(strings.Trim(objectKey, "/"), "/")
	if len(parts) != 4 || parts[0] != "media" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// safeKey rejects traversal/empty keys so tenant-scoped paths cannot escape
// their prefix.
func safeKey(key string) error {
	if key == "" || strings.Contains(key, "..") {
		return fmt.Errorf("invalid object key %q", key)
	}
	if _, err := url.Parse(key); err != nil {
		return fmt.Errorf("invalid object key %q: %w", key, err)
	}
	return nil
}
