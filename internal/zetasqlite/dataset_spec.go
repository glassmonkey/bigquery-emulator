package zetasqlite

import (
	resolved "github.com/glassmonkey/zetasql-wasm/resolved_ast"
)

// DatasetSpec is the schema-side counterpart of TableSpec /
// FunctionSpec. CREATE SCHEMA writes one of these via
// ChangedCatalog.Dataset.Added; DROP SCHEMA writes via Deleted. The
// server's syncCatalog reflects them into the metaRepo.
type DatasetSpec struct {
	NamePath   []string
	CreateMode resolved.CreateMode

	// Options carries the OPTIONS values that map to fields on
	// bigqueryv2.Dataset. Decoded at analyze time into typed Go
	// values so the server side never has to type-assert.
	Options DatasetOptions

	// UnknownOptions lists OPTIONS the emulator received but does
	// not persist. The server's syncCatalog emits a WARN log per
	// entry so silent-ignore failures are surfaced — accepting on
	// the wire (= completeness) without pretending to persist them
	// (= soundness).
	UnknownOptions []string

	// IsIfExists is set for DROP SCHEMA IF EXISTS.
	IsIfExists bool
}

// DatasetOptions holds the typed result of CREATE SCHEMA OPTIONS
// decoding. Zero values mean "not specified" — BigQuery does not
// distinguish "OPTIONS(description='')" from "OPTIONS()" at the API
// level either, so the conflation is sound for the emulator's
// REST-roundtrip contract.
type DatasetOptions struct {
	Description                    string
	FriendlyName                   string
	Location                       string
	StorageBillingModel            string
	DefaultCollation               string
	DefaultRoundingMode            string
	Labels                         map[string]string
	DefaultTableExpirationDays     int64
	DefaultPartitionExpirationDays int64
	MaxTimeTravelHours             float64
	IsCaseInsensitive              bool
}

// DatasetID returns the trailing element of NamePath, which is the
// dataset identifier whether the SQL was written as `CREATE SCHEMA
// newds` (NamePath = [newds]) or `CREATE SCHEMA p.newds` (NamePath =
// [p, newds]). Used by Conn for changed-catalog dedup so the
// "same-name double add/delete in one script" case collapses.
func (s *DatasetSpec) DatasetID() string {
	if len(s.NamePath) == 0 {
		return ""
	}
	return s.NamePath[len(s.NamePath)-1]
}

// IsIfNotExists returns true when the source CREATE SCHEMA was written
// with IF NOT EXISTS; the server uses this to suppress the "dataset
// already created" error from Project.AddDataset.
func (s *DatasetSpec) IsIfNotExists() bool {
	return s.CreateMode == resolved.CreateIfNotExistsMode
}

// IsOrReplace returns true when the source CREATE SCHEMA was written
// with OR REPLACE.
func (s *DatasetSpec) IsOrReplace() bool {
	return s.CreateMode == resolved.CreateOrReplaceMode
}
