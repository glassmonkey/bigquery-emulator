package server

import (
	"strings"
	"testing"

	"github.com/glassmonkey/bigquery-emulator/internal/zetasqlite"
)

func TestResolveDatasetIdentity(t *testing.T) {
	tests := []struct {
		name             string
		namePath         []string
		defaultProjectID string
		wantProjectID    string
		wantDatasetID    string
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
			wantProjectID:    "p",
			wantDatasetID:    "newds",
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
			wantProjectID:    "other",
			wantDatasetID:    "newds",
		},
		{
			name:          "longer path takes last as dataset and second-to-last as project",
			namePath:      []string{"folder", "p", "newds"},
			wantProjectID: "p",
			wantDatasetID: "newds",
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
			if gotProject != tt.wantProjectID {
				t.Errorf("projectID: want %q, got %q", tt.wantProjectID, gotProject)
			}
			if gotDataset != tt.wantDatasetID {
				t.Errorf("datasetID: want %q, got %q", tt.wantDatasetID, gotDataset)
			}
		})
	}
}
