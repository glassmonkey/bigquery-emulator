package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	bigqueryv2 "google.golang.org/api/bigquery/v2"
)

var ErrDuplicatedTable = errors.New("table is already created")

// Dataset carries a BigQuery dataset's identity plus its inline
// metadata blob. Like Project, it deliberately does *not* hold its
// children (tables / models / routines) in memory — each child
// row is keyed by `(projectID, datasetID, id)` in its own table
// and resolved on demand. Materialising the full child list on
// every dataset lookup was the same O(N) trap issue #90 hit on
// the projects → jobs side.
type Dataset struct {
	ID        string
	ProjectID string
	content   *bigqueryv2.Dataset
	repo      *Repository
}

func (d *Dataset) Content() *bigqueryv2.Dataset {
	return d.content
}

func (d *Dataset) UpdateContentIfExists(newContent *bigqueryv2.Dataset) {
	if newContent.Description != "" {
		d.content.Description = newContent.Description
	}
	if newContent.Etag != "" {
		d.content.Etag = newContent.Etag
	}
	if newContent.FriendlyName != "" {
		d.content.FriendlyName = newContent.FriendlyName
	}
	if d.content.IsCaseInsensitive != newContent.IsCaseInsensitive {
		d.content.IsCaseInsensitive = newContent.IsCaseInsensitive
	}
	if newContent.Kind != "" {
		d.content.Kind = newContent.Kind
	}
	if len(newContent.Labels) != 0 {
		d.content.Labels = newContent.Labels
	}
	if newContent.LastModifiedTime != 0 {
		d.content.LastModifiedTime = newContent.LastModifiedTime
	}
	if newContent.Location != "" {
		d.content.Location = newContent.Location
	}
	if newContent.MaxTimeTravelHours != 0 {
		d.content.MaxTimeTravelHours = newContent.MaxTimeTravelHours
	}
	if newContent.SatisfiesPzs {
		d.content.SatisfiesPzs = newContent.SatisfiesPzs
	}
	if newContent.SelfLink != "" {
		d.content.SelfLink = newContent.SelfLink
	}
}

func (d *Dataset) UpdateContent(newContent *bigqueryv2.Dataset) {
	d.content = newContent
}

func (d *Dataset) Insert(ctx context.Context, tx *sql.Tx) error {
	return d.repo.AddDataset(ctx, tx, d)
}

func (d *Dataset) Delete(ctx context.Context, tx *sql.Tx) error {
	return d.repo.DeleteDataset(ctx, tx, d)
}

// AddTable inserts the table row keyed by (projectID, datasetID,
// id) on the `tables` table. The parent `datasets` row is not
// touched.
func (d *Dataset) AddTable(ctx context.Context, tx *sql.Tx, table *Table) error {
	if existing, err := d.repo.FindTableWithConn(ctx, tx, d.ProjectID, d.ID, table.ID); err != nil {
		return err
	} else if existing != nil {
		return fmt.Errorf("table %s: %w", table.ID, ErrDuplicatedTable)
	}
	return table.Insert(ctx, tx)
}

// DeleteTable mirrors AddTable: child-row delete only, no parent
// row rewrite.
func (d *Dataset) DeleteTable(ctx context.Context, tx *sql.Tx, id string) error {
	table, err := d.repo.FindTableWithConn(ctx, tx, d.ProjectID, d.ID, id)
	if err != nil {
		return err
	}
	if table == nil {
		return fmt.Errorf("table '%s' is not found in dataset '%s'", id, d.ID)
	}
	return table.Delete(ctx, tx)
}

func (d *Dataset) AddModel(ctx context.Context, tx *sql.Tx, model *Model) error {
	return model.Insert(ctx, tx)
}

func (d *Dataset) DeleteModel(ctx context.Context, tx *sql.Tx, id string) error {
	model, err := d.repo.FindModelWithConn(ctx, tx, d.ProjectID, d.ID, id)
	if err != nil {
		return err
	}
	if model == nil {
		return fmt.Errorf("model '%s' is not found in dataset '%s'", id, d.ID)
	}
	return model.Delete(ctx, tx)
}

func (d *Dataset) AddRoutine(ctx context.Context, tx *sql.Tx, routine *Routine) error {
	return d.repo.AddRoutine(ctx, tx, routine)
}

func (d *Dataset) DeleteRoutine(ctx context.Context, tx *sql.Tx, id string) error {
	routine, err := d.repo.FindRoutineWithConn(ctx, tx, d.ProjectID, d.ID, id)
	if err != nil {
		return err
	}
	if routine == nil {
		return fmt.Errorf("routine '%s' is not found in dataset '%s'", id, d.ID)
	}
	return d.repo.DeleteRoutine(ctx, tx, routine)
}

// Table resolves one table by primary-key seek on the `tables`
// table — O(1) regardless of how many tables the dataset has.
func (d *Dataset) Table(ctx context.Context, id string) (*Table, error) {
	return d.repo.FindTable(ctx, d.ProjectID, d.ID, id)
}

func (d *Dataset) Model(ctx context.Context, id string) (*Model, error) {
	return d.repo.FindModel(ctx, d.ProjectID, d.ID, id)
}

func (d *Dataset) Routine(ctx context.Context, id string) (*Routine, error) {
	return d.repo.FindRoutine(ctx, d.ProjectID, d.ID, id)
}

func (d *Dataset) Tables(ctx context.Context) ([]*Table, error) {
	return d.repo.FindTablesByDataset(ctx, d.ProjectID, d.ID)
}

func (d *Dataset) Models(ctx context.Context) ([]*Model, error) {
	return d.repo.FindModelsByDataset(ctx, d.ProjectID, d.ID)
}

func (d *Dataset) Routines(ctx context.Context) ([]*Routine, error) {
	return d.repo.FindRoutinesByDataset(ctx, d.ProjectID, d.ID)
}

// NewDataset builds an in-memory Dataset identity. Tables / models
// / routines are *not* retained on the struct; they are resolved
// lazily through the repository. The variadic tail is accepted
// for source compatibility with the old signature but ignored —
// callers should write children through AddTable / AddModel /
// AddRoutine which insert into the child tables directly.
func NewDataset(
	repo *Repository,
	projectID string,
	datasetID string,
	content *bigqueryv2.Dataset,
	_ []*Table,
	_ []*Model,
	_ []*Routine,
) *Dataset {
	return &Dataset{
		ID:        datasetID,
		ProjectID: projectID,
		content:   content,
		repo:      repo,
	}
}
