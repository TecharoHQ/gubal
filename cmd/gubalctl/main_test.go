package main

import (
	"reflect"
	"testing"
)

func TestParseVersions(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		input   string
		want    []int32
		wantErr bool
	}{
		{name: "single", input: "150", want: []int32{150}},
		{name: "multiple", input: "120,150", want: []int32{120, 150}},
		{name: "trims and drops empties", input: " 120 , ,150,", want: []int32{120, 150}},
		{name: "empty is an error", input: "", wantErr: true},
		{name: "whitespace only is an error", input: "  , ", wantErr: true},
		{name: "non-numeric is an error", input: "120,abc", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseVersions(tt.input)
			if (err != nil) != tt.wantErr {
				t.Logf("want error: %v", tt.wantErr)
				t.Logf("got:  %v", err)
				t.Fatal("got wrong error state")
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Logf("want: %v", tt.want)
				t.Logf("got:  %v", got)
				t.Fatal("wrong result")
			}
		})
	}
}
