package smoketest

import (
	"context"
	"testing"
)

func TestBundleKey(t *testing.T) {
	t.Parallel()
	if got := bundleKey(1741, "abc-uuid"); got != "pr-1741/abc-uuid.zip" {
		t.Fatalf("got %q", got)
	}
}

func TestNoopUploader(t *testing.T) {
	t.Parallel()
	url, err := noopUploader{}.Upload(context.Background(), "k", []byte("x"), "application/zip")
	if err != nil || url != "" {
		t.Fatalf("noop = %q, %v", url, err)
	}
}

func TestNewTigrisUploaderNoBucket(t *testing.T) {
	t.Parallel()
	u, err := NewTigrisUploader(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := u.(noopUploader); !ok {
		t.Fatalf("want noopUploader with empty bucket, got %T", u)
	}
}
