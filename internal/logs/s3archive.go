package logs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type s3Archive struct {
	client *s3.Client
	bucket string
}

// NewS3Archive stores archives as objects in bucket, encrypted with
// S3-managed keys (SSE-S3). The client is Yumdrake's own, not a connected
// account's. The Archive contract carries no checksum, and archives are
// gzip streams that fail to decode when damaged, so none is kept here.
func NewS3Archive(client *s3.Client, bucket string) Archive {
	return &s3Archive{client: client, bucket: bucket}
}

// validateKey applies the same rules as the directory archive: a clean,
// relative, slash-separated key.
func validateKey(key string) error {
	if key == "" || strings.Contains(key, "\\") || path.Clean("/"+key) != "/"+key {
		return fmt.Errorf("log archive: invalid key %q", key)
	}
	return nil
}

// Put writes the object; a repeated Put of the same key replaces it.
func (a *s3Archive) Put(ctx context.Context, key string, data []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := a.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(a.bucket),
		Key:                  aws.String(key),
		Body:                 bytes.NewReader(data),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	})
	if err != nil {
		return fmt.Errorf("log archive: put %s: %w", key, err)
	}
	return nil
}

func (a *s3Archive) Get(ctx context.Context, key string) ([]byte, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	out, err := a.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(a.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("log archive: read %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("log archive: read %s: %w", key, err)
	}
	return data, nil
}
