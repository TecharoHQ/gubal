package main

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
			got, err := parseVersions(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
