package config

import (
	"reflect"
	"testing"
)

func TestParseBuildDir(t *testing.T) {
	tests := []struct {
		spec    string
		want    BuildDirSpec
		wantErr bool
	}{
		{spec: "gen", want: BuildDirSpec{Name: "gen"}},
		{spec: "gen:Makefile", want: BuildDirSpec{Name: "gen", Markers: []string{"Makefile"}}},
		{spec: "gen:a.txt,*.cfg", want: BuildDirSpec{Name: "gen", Markers: []string{"a.txt", "*.cfg"}}},
		{spec: "*.egg-info:setup.py", want: BuildDirSpec{Name: "*.egg-info", Markers: []string{"setup.py"}}},
		{spec: ".angular/cache", want: BuildDirSpec{Name: ".angular/cache"}},
		{spec: "", wantErr: true},
		{spec: ":x", wantErr: true},
		{spec: "a/b/c", wantErr: true},
		{spec: "/abs", wantErr: true},
		{spec: "..", wantErr: true},
		{spec: `a\b`, wantErr: true},
		{spec: "gen:", wantErr: true},
		{spec: "gen:a,,b", wantErr: true},
		{spec: "gen:dir/file", wantErr: true},
		{spec: "[x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got, err := ParseBuildDir(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
