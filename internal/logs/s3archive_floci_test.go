//go:build integration && floci

package logs

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/rcss-labs/yamdrake/test/awstest"
)

// newS3Archive seeds a fresh account and bucket and returns an archive over them.
func newS3Archive(t *testing.T) (Archive, *awstest.Account, string, *s3.Client) {
	t.Helper()
	w := awstest.New(t)
	acct := w.Account(awstest.RandomAccountID())
	bucket := "yd-logs-" + awstest.RandomSuffix()
	acct.Bucket(bucket, false)
	client := s3.NewFromConfig(acct.AWSConfig("us-east-1"), func(o *s3.Options) { o.UsePathStyle = true })
	return NewS3Archive(client, bucket), acct, bucket, client
}

func TestS3ArchivePutGet(t *testing.T) {
	ctx := context.Background()
	archive, acct, bucket, client := newS3Archive(t)
	key := archiveKey(7, 9)
	data := []byte{0x1f, 0x8b, 0x08, 0x00, 'l', 'o', 'g'}

	if err := archive.Put(ctx, key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, ok := acct.Object(bucket, key); !ok || !bytes.Equal(got, data) {
		t.Errorf("stored object = %q, %v", got, ok)
	}
	got, err := archive.Get(ctx, key)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, %v", got, err)
	}
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if head.ServerSideEncryption != s3types.ServerSideEncryptionAes256 {
		t.Errorf("ServerSideEncryption = %q, want AES256", head.ServerSideEncryption)
	}

	// A repeated Put of the same key replaces the object.
	if err := archive.Put(ctx, key, []byte("second")); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if got, err := archive.Get(ctx, key); err != nil || string(got) != "second" {
		t.Fatalf("Get after replace = %q, %v", got, err)
	}
	if _, err := archive.Get(ctx, archiveKey(7, 10)); err == nil {
		t.Error("Get of a missing key succeeded")
	}
	for _, bad := range []string{"", "../x", "/abs", "a//b", `a\b`} {
		if err := archive.Put(ctx, bad, data); err == nil {
			t.Errorf("Put(%q) succeeded, want an invalid-key error", bad)
		}
	}
}

// TestArchiverWorksWithS3Archive runs the archive-then-read scenario of
// TestArchiveThenReadIsIdenticalToBefore against the S3 archive.
func TestArchiverWorksWithS3Archive(t *testing.T) {
	e := newEnv(t, 3)
	ctx := context.Background()
	seedForArchive(t, e)
	settle(t, e, "succeeded")
	archive, acct, bucket, _ := newS3Archive(t)

	r := NewReader(e.pool, archive)
	before := readEverything(t, r, e.runID)
	if len(before.Lines) == 0 {
		t.Fatal("nothing to archive")
	}
	now := baseTime.Add(48 * time.Hour)
	ar := NewArchiver(e.pool, archive, time.Hour, func() time.Time { return now }, nil)
	n, err := ar.ArchiveSettledLegs(ctx)
	if err != nil || n != 2 {
		t.Fatalf("ArchiveSettledLegs = %d, %v; want 2 legs", n, err)
	}
	for i := 0; i < 2; i++ {
		if left := e.stored(t, e.legs[i].ID); len(left) != 0 {
			t.Fatalf("leg %d still has %d rows in PostgreSQL", i, len(left))
		}
		if _, ok := acct.Object(bucket, archiveKey(e.runID, e.legs[i].ID)); !ok {
			t.Errorf("leg %d has no object in the bucket", i)
		}
	}
	after := readEverything(t, r, e.runID)
	if !reflect.DeepEqual(normalize(before.Lines), normalize(after.Lines)) || len(after.Gaps) != 0 {
		t.Fatalf("read after archiving differs:\nbefore %+v\nafter  %+v", before.Lines, after.Lines)
	}
}
