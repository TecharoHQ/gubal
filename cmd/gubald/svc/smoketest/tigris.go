package smoketest

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	storage "github.com/tigrisdata/storage-go"
)

// bundleUploader stores a report bundle and returns a public URL to it.
type bundleUploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) (string, error)
}

// tigrisUploader uploads public-read objects to a Tigris bucket and returns their
// permanent public URL.
type tigrisUploader struct {
	client *storage.Client
	bucket string
}

// NewTigrisUploader builds a Tigris-backed uploader using the standard AWS
// credential chain (env AWS_*, ~/.aws config/credentials, container/instance
// roles). An empty bucket returns a noopUploader, disabling bundle uploads.
func NewTigrisUploader(ctx context.Context, bucket string) (bundleUploader, error) {
	if bucket == "" {
		return noopUploader{}, nil
	}
	client, err := storage.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("building tigris client: %w", err)
	}
	return &tigrisUploader{client: client, bucket: bucket}, nil
}

// Upload stores data at key as a public-read object and returns its public URL.
func (u *tigrisUploader) Upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(u.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
		ACL:         types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		return "", fmt.Errorf("uploading %s to %s: %w", key, u.bucket, err)
	}
	return fmt.Sprintf("https://%s.t3.storage.dev/%s", u.bucket, key), nil
}

// noopUploader is used when no Tigris credentials are configured: uploads are
// skipped and no URL is produced.
type noopUploader struct{}

// Upload does nothing and returns an empty URL and no error.
func (noopUploader) Upload(context.Context, string, []byte, string) (string, error) {
	return "", nil
}

// bundleKey is the object key for a run's bundle: pr-<pr>/<id>.zip. id is the
// request UUID (unique per run), so re-runs never collide.
func bundleKey(pr int, id string) string {
	return fmt.Sprintf("pr-%d/%s.zip", pr, id)
}
