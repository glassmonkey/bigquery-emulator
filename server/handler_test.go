package server

import (
	"strings"
	"testing"

	"github.com/glassmonkey/bigquery-emulator/internal/zetasqlite"
	"github.com/google/go-cmp/cmp"
)

func TestResolveDatasetIdentity(t *testing.T) {
	// identity packs (projectID, datasetID) so each happy case
	// asserts on a single value (R3 / Assertion Roulette).
	type identity struct {
		ProjectID string
		DatasetID string
	}
	tests := []struct {
		name             string
		namePath         []string
		defaultProjectID string
		want             identity
		wantErrSubstr    string
	}{
		{
			name:          "empty NamePath is an error",
			namePath:      nil,
			wantErrSubstr: "empty NamePath",
		},
		{
			name:             "bare name falls back to defaultProjectID",
			namePath:         []string{"newds"},
			defaultProjectID: "p",
			want:             identity{ProjectID: "p", DatasetID: "newds"},
		},
		{
			name:          "bare name without defaultProjectID is an error",
			namePath:      []string{"newds"},
			wantErrSubstr: "bare SCHEMA name",
		},
		{
			name:             "project-qualified name wins over defaultProjectID",
			namePath:         []string{"other", "newds"},
			defaultProjectID: "p",
			want:             identity{ProjectID: "other", DatasetID: "newds"},
		},
		{
			name:     "longer path takes last as dataset and second-to-last as project",
			namePath: []string{"folder", "p", "newds"},
			want:     identity{ProjectID: "p", DatasetID: "newds"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &zetasqlite.DatasetSpec{NamePath: tt.namePath}
			gotProject, gotDataset, err := resolveDatasetIdentity(spec, tt.defaultProjectID)
			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := identity{ProjectID: gotProject, DatasetID: gotDataset}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("identity (-want +got):\n%s", diff)
			}
		})
	}
}
