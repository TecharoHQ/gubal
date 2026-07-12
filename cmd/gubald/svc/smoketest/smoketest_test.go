package smoketest

import (
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	gubalv1 "github.com/TecharoHQ/gubal/gen/techaro/lol/gubal/v1"
	"github.com/google/uuid"
)

// TestSmokeTestRequestValidation exercises the same protovalidate rules the
// handler runs, guarding against invalid buf.validate options (e.g. scalar rules
// on a repeated field, which fail to compile at validation time).
func TestSmokeTestRequestValidation(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		req     *gubalv1.SmokeTestRequest
		wantErr bool
	}{
		{
			name: "valid",
			req:  &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "ghcr.io/techarohq/anubis:latest", ChromeVersions: []int32{120, 150}, FirefoxVersions: []int32{140, 152}},
		},
		{
			name:    "empty versions",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: nil},
			wantErr: true,
		},
		{
			name:    "version too low",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{50}},
			wantErr: true,
		},
		{
			name:    "bad uuid",
			req:     &gubalv1.SmokeTestRequest{Id: "not-a-uuid", AnubisImage: "x", ChromeVersions: []int32{120}},
			wantErr: true,
		},
		{
			name:    "missing anubis image",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), ChromeVersions: []int32{120}},
			wantErr: true,
		},
		{
			name:    "missing firefox versions",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}},
			wantErr: true,
		},
		{
			name:    "firefox version too low",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{100}},
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := protovalidate.Validate(tt.req)
			if (err != nil) != tt.wantErr {
				t.Logf("want error: %v", tt.wantErr)
				t.Logf("got:  %v", err)
				t.Fatal("wrong validation result")
			}
		})
	}
}

func TestBrowsersFor(t *testing.T) {
	req := &gubalv1.SmokeTestRequest{ChromeVersions: []int32{120, 150}, FirefoxVersions: []int32{140, 152}}
	bs, err := browsersFor(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 || bs[0].Name != "chrome" || bs[1].Name != "firefox" {
		t.Fatalf("browsers = %+v", bs)
	}
	if strings.Join(bs[0].Versions, ",") != "120,150" {
		t.Fatalf("chrome versions = %v", bs[0].Versions)
	}
	if strings.Join(bs[1].Versions, ",") != "140,152" {
		t.Fatalf("firefox versions = %v", bs[1].Versions)
	}
}
