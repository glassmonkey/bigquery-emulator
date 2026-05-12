// mergefixture combines a per-caseset fixture directory into a
// single emulator-compatible YAML that bigquery-emulator loads via
// --data-from-yaml.
//
// Usage:
//
//	go run ./e2e/cmd/mergefixture <fixture-dir> <out.yml>
//
// Fixture directory layout:
//
//	<fixture-dir>/
//	├── schema.yml                       columns per table, in the
//	│                                    emulator-native projects ▶
//	│                                    datasets ▶ tables ▶ columns
//	│                                    shape.
//	└── data/
//	    └── <project>.<dataset>.yml      flat map: table id → rows.
//	                                     `<project>` and `<dataset>`
//	                                     are recovered from the file
//	                                     name by splitting on the
//	                                     last '.' (BigQuery's project
//	                                     ID and dataset ID rules
//	                                     forbid '.' so this is
//	                                     unambiguous).
//
// Schema is the source of truth for structure; rows whose table is
// absent from the schema are dropped. Tables present in the schema
// but absent from data/ are emitted with no `data:` field (created
// empty).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// dataIndex is project → dataset → table → rows.
type dataIndex map[string]map[string]map[string][]map[string]interface{}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mergefixture <fixture-dir> <out.yml>")
		os.Exit(2)
	}
	dir, outPath := os.Args[1], os.Args[2]

	schema, err := readRoot(filepath.Join(dir, "schema.yml"))
	if err != nil {
		die("read schema:", err)
	}
	idx, err := readDataDir(filepath.Join(dir, "data"))
	if err != nil {
		die("read data dir:", err)
	}

	if err := writeRoot(outPath, mergeRoot(schema, idx)); err != nil {
		die("write out:", err)
	}
}

// readDataDir globs <dir>/*.yml. Each file's basename (sans .yml)
// is split on the last '.' to recover "<project>.<dataset>"; the
// file contents are a flat map: table id → rows.
func readDataDir(dir string) (dataIndex, error) {
	idx := dataIndex{}
	files, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".yml")
		dot := strings.LastIndex(base, ".")
		if dot <= 0 || dot == len(base)-1 {
			return nil, fmt.Errorf("%s: filename must be <project>.<dataset>.yml", f)
		}
		proj := base[:dot]
		ds := base[dot+1:]

		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var tables map[string][]map[string]interface{}
		if err := yaml.Unmarshal(b, &tables); err != nil {
			return nil, fmt.Errorf("unmarshal %s: %w", f, err)
		}
		if idx[proj] == nil {
			idx[proj] = map[string]map[string][]map[string]interface{}{}
		}
		idx[proj][ds] = tables
	}
	return idx, nil
}

func mergeRoot(schema root, idx dataIndex) root {
	out := root{}
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
		out.Projects = append(out.Projects, mp)
	}
	return out
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
