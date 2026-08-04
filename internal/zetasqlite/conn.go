package zetasqlite

import (
	"context"
	"database/sql"
)

type ChangedCatalog struct {
	Table    *ChangedTable
	Function *ChangedFunction
	Dataset  *ChangedDataset
}

func newChangedCatalog() *ChangedCatalog {
	return &ChangedCatalog{
		Table:    &ChangedTable{},
		Function: &ChangedFunction{},
		Dataset:  &ChangedDataset{},
	}
}

func (c *ChangedCatalog) Changed() bool {
	return c.Table.Changed() || c.Function.Changed() || c.Dataset.Changed()
}

type ChangedTable struct {
	Added   []*TableSpec
	Updated []*TableSpec
	Deleted []*TableSpec
}

func (t *ChangedTable) Changed() bool {
	return len(t.Added) != 0 || len(t.Updated) != 0 || len(t.Deleted) != 0
}

type ChangedFunction struct {
	Added   []*FunctionSpec
	Deleted []*FunctionSpec
}

func (f *ChangedFunction) Changed() bool {
	return len(f.Added) != 0 || len(f.Deleted) != 0
}

// ChangedDataset carries CREATE SCHEMA / DROP SCHEMA effects observed
// during a statement. The server's syncCatalog reads these to
// insert/delete dataset metadata in the metaRepo, mirroring how
// table/function changes flow.
type ChangedDataset struct {
	Added   []*DatasetSpec
	Deleted []*DatasetSpec
}

func (d *ChangedDataset) Changed() bool {
	return len(d.Added) != 0 || len(d.Deleted) != 0
}

type Conn struct {
	conn *sql.Conn
	tx   *sql.Tx
	cc   *ChangedCatalog
}

func NewConn(conn *sql.Conn, tx *sql.Tx) *Conn {
	return &Conn{
		conn: conn,
		tx:   tx,
		cc:   newChangedCatalog(),
	}
}

func (c *Conn) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if c.tx != nil {
		return c.tx.PrepareContext(ctx, query)
	}
	return c.conn.PrepareContext(ctx, query)
}

func (c *Conn) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if c.tx != nil {
		return c.tx.ExecContext(ctx, query, args...)
	}
	return c.conn.ExecContext(ctx, query, args...)
}

func (c *Conn) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if c.tx != nil {
		return c.tx.QueryContext(ctx, query, args...)
	}
	return c.conn.QueryContext(ctx, query, args...)
}

func (c *Conn) addTable(spec *TableSpec) {
	c.removeFromDeletedTablesIfExists(spec)
	c.cc.Table.Added = append(c.cc.Table.Added, spec)
}

//nolint:unused
func (c *Conn) updateTable(spec *TableSpec) {
	c.cc.Table.Updated = append(c.cc.Table.Updated, spec)
}

func (c *Conn) deleteTable(spec *TableSpec) {
	c.removeFromAddedTablesIfExists(spec)
	c.cc.Table.Deleted = append(c.cc.Table.Deleted, spec)
}

func (c *Conn) addFunction(spec *FunctionSpec) {
	c.removeFromDeletedFunctionsIfExists(spec)
	c.cc.Function.Added = append(c.cc.Function.Added, spec)
}

func (c *Conn) deleteFunction(spec *FunctionSpec) {
	c.removeFromAddedFunctionsIfExists(spec)
	c.cc.Function.Deleted = append(c.cc.Function.Deleted, spec)
}

func (c *Conn) removeFromDeletedTablesIfExists(spec *TableSpec) {
	tables := make([]*TableSpec, 0, len(c.cc.Table.Deleted))
	for _, table := range c.cc.Table.Deleted {
		if table.TableName() == spec.TableName() {
			continue
		}
		tables = append(tables, table)
	}
	c.cc.Table.Deleted = tables
}

func (c *Conn) removeFromAddedTablesIfExists(spec *TableSpec) {
	tables := make([]*TableSpec, 0, len(c.cc.Table.Added))
	for _, table := range c.cc.Table.Added {
		if table.TableName() == spec.TableName() {
			continue
		}
		tables = append(tables, table)
	}
	c.cc.Table.Added = tables
}

func (c *Conn) removeFromDeletedFunctionsIfExists(spec *FunctionSpec) {
	funcs := make([]*FunctionSpec, 0, len(c.cc.Function.Deleted))
	for _, fun := range c.cc.Function.Deleted {
		if fun.FuncName() == spec.FuncName() {
			continue
		}
		funcs = append(funcs, fun)
	}
	c.cc.Function.Deleted = funcs
}

func (c *Conn) removeFromAddedFunctionsIfExists(spec *FunctionSpec) {
	funcs := make([]*FunctionSpec, 0, len(c.cc.Function.Added))
	for _, fun := range c.cc.Function.Added {
		if fun.FuncName() == spec.FuncName() {
			continue
		}
		funcs = append(funcs, fun)
	}
	c.cc.Function.Added = funcs
}

func (c *Conn) addDataset(spec *DatasetSpec) {
	c.removeFromDeletedDatasetsIfExists(spec)
	c.cc.Dataset.Added = append(c.cc.Dataset.Added, spec)
}

func (c *Conn) deleteDataset(spec *DatasetSpec) {
	c.removeFromAddedDatasetsIfExists(spec)
	c.cc.Dataset.Deleted = append(c.cc.Dataset.Deleted, spec)
}

func (c *Conn) removeFromDeletedDatasetsIfExists(spec *DatasetSpec) {
	datasets := make([]*DatasetSpec, 0, len(c.cc.Dataset.Deleted))
	for _, ds := range c.cc.Dataset.Deleted {
		if ds.DatasetID() == spec.DatasetID() {
			continue
		}
		datasets = append(datasets, ds)
	}
	c.cc.Dataset.Deleted = datasets
}

func (c *Conn) removeFromAddedDatasetsIfExists(spec *DatasetSpec) {
	datasets := make([]*DatasetSpec, 0, len(c.cc.Dataset.Added))
	for _, ds := range c.cc.Dataset.Added {
		if ds.DatasetID() == spec.DatasetID() {
			continue
		}
		datasets = append(datasets, ds)
	}
	c.cc.Dataset.Added = datasets
}
