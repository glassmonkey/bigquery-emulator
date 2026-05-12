//go:build e2e

// Smoke tests an already-running bigquery-emulator container.
// Assumes the emulator is reachable on http://localhost:9050 — bring
// it up with `make docker/up` before running. Iterates every
// testdata/query/*.sql, posts it to the BigQuery REST query
// endpoint, and diffs the response against the paired golden file.
//
// Each query has exactly one of:
//
//	<name>.golden.tsv  — expect HTTP 200; render schema/rows as TSV
//	                     (line 1 = column names, line 2..N = row
//	                     values, both \t-joined) and byte-compare.
//	<name>.golden.err  — expect HTTP non-200; substring-match the
//	                     emulator's error.message against the file
//	                     contents.
//
// testdata layout:
//
//	e2e/testdata/
//	├── fixture/                  pre-condition state loaded into
//	│   └── seed.yml              the emulator via --data-from-yaml
//	│                             (bind-mounted by compose.yml at
//	│                             /fixture/seed.yml)
//	└── query/
//	    ├── <name>.sql            query to POST
//	    ├── <name>.golden.tsv     OR
//	    └── <name>.golden.err
//
// Run via the Makefile (the `make e2e` target wires the
// docker/healthcheck gate so the test fails fast with a clear
// message if the emulator is not up):
//
//	make docker/up
//	make e2e
//	make docker/down
//
// Or directly:
//
//	go test -tags=e2e -count=1 -v ./e2e/...
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	composeFile = "compose.yml"
	project     = "test"
	httpAddr    = "http://localhost:9050"
)

// TestSmoke posts every testdata/query/*.sql to the running emulator
// and diffs the response against the paired golden file. The
// container's lifecycle is the user's responsibility (see
// `make docker/up` / `make docker/down`).
func TestSmoke(t *testing.T) {
	cases, err := filepath.Glob("testdata/query/*.sql")
	if err != nil {
		t.Fatalf("glob testdata/query: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("no testdata/query/*.sql files found")
	}

	for _, sqlPath := range cases {
		sqlPath := sqlPath
		name := strings.TrimSuffix(filepath.Base(sqlPath), ".sql")
		tsvPath := strings.TrimSuffix(sqlPath, ".sql") + ".golden.tsv"
		errPath := strings.TrimSuffix(sqlPath, ".sql") + ".golden.err"

		t.Run(name, func(t *testing.T) {
			sqlBytes, err := os.ReadFile(sqlPath)
			if err != nil {
				t.Fatalf("read %s: %v", sqlPath, err)
			}
			sql := strings.TrimSpace(string(sqlBytes))

			tsvExists := fileExists(tsvPath)
			errExists := fileExists(errPath)
			if tsvExists && errExists {
				t.Fatalf("both %s and %s exist; only one golden is allowed per query", tsvPath, errPath)
			}

			// Act
			status, body := postQuery(t, sql)

			// Assert
			if errExists {
				assertErrorGolden(t, errPath, status, body)
				return
			}
			assertSuccessGolden(t, tsvPath, status, body)
		})
	}
}

// queryResponse decodes the synchronous query endpoint shape we care
// about. The full response carries more fields (jobReference,
// totalRows, ...) but the smoke harness only needs the schema field
// names and the row values to render TSV.
type queryResponse struct {
	Schema struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	} `json:"schema"`
	Rows []struct {
		F []struct {
			V string `json:"v"`
		} `json:"f"`
	} `json:"rows"`
}

// errorResponse decodes the BigQuery REST error envelope produced
// by bigquery-emulator/server/error.go.
type errorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// renderTSV turns a query response into a tab-separated string with
// the column-name header on the first line and row values on the
// subsequent lines. Always ends with a trailing newline so the
// golden file's editor-friendly trailing newline is preserved on a
// byte-for-byte compare.
func renderTSV(resp queryResponse) string {
	var sb strings.Builder
	headers := make([]string, 0, len(resp.Schema.Fields))
	for _, f := range resp.Schema.Fields {
		headers = append(headers, f.Name)
	}
	sb.WriteString(strings.Join(headers, "\t"))
	sb.WriteByte('\n')
	for _, row := range resp.Rows {
		values := make([]string, 0, len(row.F))
		for _, c := range row.F {
			values = append(values, c.V)
		}
		sb.WriteString(strings.Join(values, "\t"))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// assertSuccessGolden expects HTTP 200 and a JSON queryResponse
// body. Renders the response as TSV and diffs against the golden
// file at path; creates the golden on first run if it does not
// exist (see assertGolden).
func assertSuccessGolden(t *testing.T, path string, status int, body []byte) {
	t.Helper()
	if status != http.StatusOK {
		dumpLogs(t)
		t.Fatalf("expected HTTP 200, got %d body=%s", status, body)
	}
	var resp queryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, body)
	}
	assertGolden(t, path, renderTSV(resp))
}

// assertErrorGolden expects HTTP non-200 and a BigQuery error
// envelope. Substring-matches error.message against the golden
// file at path (callers store only the stable subset of the
// message — substring rather than exact match because zetasql/
// emulator error wording is not load-bearing).
func assertErrorGolden(t *testing.T, path string, status int, body []byte) {
	t.Helper()
	if status == http.StatusOK {
		t.Fatalf("expected HTTP non-200 (golden.err pinned), got 200 body=%s", body)
	}
	var resp errorResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, body)
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	want := strings.TrimSpace(string(wantBytes))
	if !strings.Contains(resp.Error.Message, want) {
		t.Errorf("error.message does not contain golden (%s)\n--- got\n%s\n--- want (substring)\n%s",
			path, resp.Error.Message, want)
	}
}

// assertGolden compares `got` against the golden file at path. On
// the first run for a freshly-added query the file does not exist
// yet — in that case the helper writes `got` as the new golden,
// logs the creation, and returns without flagging the test as
// failed. Subsequent runs hit the diff branch.
func assertGolden(t *testing.T, path, got string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("create golden file %s: %v", path, err)
		}
		t.Logf("created %s — review and commit; next run will diff against it", path)
		return
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if want := string(raw); got != want {
		t.Errorf("response mismatch (%s)\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// postQuery POSTs the SQL to /projects/<project>/queries and returns
// the HTTP status code and raw response body. The caller decides
// whether the status was expected — error-path tests want a non-200,
// success-path tests want a 200. Dumps the last 50 lines of the
// container's logs on transport failures so a single test run
// carries the diagnostic trail.
func postQuery(t *testing.T, sql string) (int, []byte) {
	t.Helper()
	url := fmt.Sprintf("%s/projects/%s/queries", httpAddr, project)
	body, err := json.Marshal(map[string]any{
		"query":        sql,
		"useLegacySql": false,
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		dumpLogs(t)
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, raw
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dumpLogs prints the last 50 lines of the emulator's container
// logs to the test output. Best-effort: if `docker compose` is not
// on PATH or the compose stack is not running, the failure stays
// silent (the higher-level assertion already reported the cause).
func dumpLogs(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-f", composeFile, "logs", "--tail=50", "bigquery-emulator")
	cmd.Stdout = testLogWriter{t}
	cmd.Stderr = testLogWriter{t}
	_ = cmd.Run()
}

// testLogWriter routes a child process's stdout / stderr into the
// test's structured log so every line is attributable to the test
// run.
type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
