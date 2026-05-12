// mergefixture combines a per-caseset schema.yml (columns-only) and
// data.yml (rows-only) into a single emulator-compatible fixture
// YAML that bigquery-emulator can load via --data-from-yaml.
//
// Usage:
//
//	go run ./e2e/cmd/mergefixture <schema.yml> <data.yml> <out.yml>
//
// The caseset's project / dataset / table identifiers must match
// between schema.yml and data.yml. Rows whose table is absent from
// the schema are silently dropped (schema is the source of truth
// for structure); columns whose table is absent from the data are
// kept with an empty data slice (table is created empty).
package main

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

type table struct {
	ID      string                   `yaml:"id"`
	Columns []map[string]interface{} `yaml:"columns,omitempty"`
	Data    []map[string]interface{} `yaml:"data,omitempty"`
}

type dataset struct {
	ID     string  `yaml:"id"`
	Tables []table `yaml:"tables"`
}

type project struct {
	ID       string    `yaml:"id"`
	Datasets []dataset `yaml:"datasets"`
}

type root struct {
	Projects []project `yaml:"projects"`
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: mergefixture <schema.yml> <data.yml> <out.yml>")
		os.Exit(2)
	}
	schemaPath, dataPath, outPath := os.Args[1], os.Args[2], os.Args[3]

	schema, err := readRoot(schemaPath)
	if err != nil {
		die("read schema:", err)
	}
	data, err := readRoot(dataPath)
	if err != nil {
		die("read data:", err)
	}

	if err := writeRoot(outPath, mergeRoot(schema, data)); err != nil {
		die("write out:", err)
	}
}

func mergeRoot(schema, data root) root {
	idx := buildDataIndex(data)
	merged := root{}
	for _, p := range schema.Projects {
		mp := project{ID: p.ID}
		for _, d := range p.Datasets {
			md := dataset{ID: d.ID}
			for _, tbl := range d.Tables {
				mt := table{ID: tbl.ID, Columns: tbl.Columns}
				if rows := idx[p.ID][d.ID][tbl.ID]; len(rows) > 0 {
					mt.Data = rows
				}
				md.Tables = append(md.Tables, mt)
			}
			mp.Datasets = append(mp.Datasets, md)
		}
		merged.Projects = append(merged.Projects, mp)
	}
	return merged
}

func buildDataIndex(r root) map[string]map[string]map[string][]map[string]interface{} {
	idx := map[string]map[string]map[string][]map[string]interface{}{}
	for _, p := range r.Projects {
		if idx[p.ID] == nil {
			idx[p.ID] = map[string]map[string][]map[string]interface{}{}
		}
		for _, d := range p.Datasets {
			if idx[p.ID][d.ID] == nil {
				idx[p.ID][d.ID] = map[string][]map[string]interface{}{}
			}
			for _, tbl := range d.Tables {
				idx[p.ID][d.ID][tbl.ID] = tbl.Data
			}
		}
	}
	return idx
}

func readRoot(path string) (root, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return root{}, err
	}
	var r root
	if err := yaml.Unmarshal(b, &r); err != nil {
		return root{}, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return r, nil
}

func writeRoot(path string, r root) error {
	b, err := yaml.MarshalWithOptions(r, yaml.Indent(2))
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func die(prefix string, err error) {
	fmt.Fprintln(os.Stderr, prefix, err)
	os.Exit(1)
}
