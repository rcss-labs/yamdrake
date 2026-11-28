// Package artifacts keeps copies of the configuration files a run touched in
// an encrypted S3 bucket, and verifies their SHA-256 on every read.
package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrCorrupt means the stored bytes do not hash to the expected SHA-256.
var ErrCorrupt = errors.New("artifacts: stored object does not match its checksum")

const keyPrefix = "manifests/"

// Store reads and writes config copies in one bucket.
type Store struct {
	client   *s3.Client
	bucket   string
	kmsKeyID string
}

// New returns a Store. With a non-empty kmsKeyID objects are encrypted with
// that KMS key; otherwise with S3-managed keys (SSE-S3).
func New(client *s3.Client, bucket, kmsKeyID string) *Store {
	return &Store{client: client, bucket: bucket, kmsKeyID: kmsKeyID}
}

// Put stores data under manifests/<run>/<service>/<kind>/<name>, or under
// .../<kind>/previous/<name> when previous is true (the prior destination
// content). It returns the key and the hex SHA-256 of data.
func (s *Store) Put(ctx context.Context, runID int64, service, kind, name string, previous bool, data []byte) (key, sha256hex string, err error) {
	for field, value := range map[string]string{"service": service, "kind": kind, "name": name} {
		if err := validComponent(value); err != nil {
			return "", "", fmt.Errorf("artifacts: invalid %s: %w", field, err)
		}
	}
	key = fmt.Sprintf("%s%d/%s/%s/", keyPrefix, runID, service, kind)
	if previous {
		key += "previous/"
	}
	key += name

	sum := sha256.Sum256(data)
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}
	if s.kmsKeyID != "" {
		input.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
		input.SSEKMSKeyId = aws.String(s.kmsKeyID)
	} else {
		input.ServerSideEncryption = s3types.ServerSideEncryptionAes256
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return "", "", fmt.Errorf("artifacts: put %s: %w", key, err)
	}
	return key, hex.EncodeToString(sum[:]), nil
}

// Get returns the object's bytes after checking they hash to wantSHA256
// (hex). It returns ErrCorrupt, and no bytes, when they do not.
func (s *Store) Get(ctx context.Context, key, wantSHA256 string) ([]byte, error) {
	if !strings.HasPrefix(key, keyPrefix) || strings.Contains(key, "..") {
		return nil, fmt.Errorf("artifacts: key %q is outside %s", key, keyPrefix)
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("artifacts: get %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("artifacts: read %s: %w", key, err)
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), wantSHA256) {
		return nil, fmt.Errorf("%w: %s", ErrCorrupt, key)
	}
	return data, nil
}

// validComponent rejects values that could change the key layout.
func validComponent(v string) error {
	switch {
	case v == "":
		return errors.New("must not be empty")
	case strings.Contains(v, "/"):
		return errors.New("must not contain '/'")
	case strings.Contains(v, ".."):
		return errors.New("must not contain '..'")
	}
	return nil
}
