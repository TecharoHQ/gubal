package chromesweep

import (
	"reflect"
	"testing"
)

func TestParseVersions(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{"basic", []string{"110", "120", "150"}, []string{"110", "120", "150"}, false},
		{"trims and drops empties", []string{" 110 ", "", "120"}, []string{"110", "120"}, false},
		{"rejects duplicates", []string{"110", "110"}, nil, true},
		{"empty is an error", []string{"", "  "}, nil, true},
		{"no args is an error", nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVersions(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReportedUA(t *testing.T) {
	logs := "waiting for chrome:9222 ...\nUser-Agent: Mozilla/5.0 Chrome/150.0.0.0 Safari/537.36\nPASS: driveable, non-Headless UA\n"
	if got, want := reportedUA(logs), "Mozilla/5.0 Chrome/150.0.0.0 Safari/537.36"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := reportedUA("no ua here"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestCapturedFramePath(t *testing.T) {
	ok := `{"level":"INFO","msg":"connected to chrome"}
{"level":"INFO","msg":"captured","path":"/data/chrome-150.0.7871.114-20260101T000000.000Z.png"}`
	got, err := capturedFramePath(ok)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := "/data/chrome-150.0.7871.114-20260101T000000.000Z.png"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	fatal := `{"level":"INFO","msg":"connected to chrome"}
{"level":"ERROR","msg":"fatal","err":"timed out waiting for text"}`
	if _, err := capturedFramePath(fatal); err == nil {
		t.Fatalf("expected error from fatal log")
	}

	if _, err := capturedFramePath("garbage\n"); err == nil {
		t.Fatalf("expected error when no capture line present")
	}
}
