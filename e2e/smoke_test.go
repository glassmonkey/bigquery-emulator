//go:build e2e

// Smoke tests the bigquery-emulator container image end to end.
// Build the image from the top-level Dockerfile, bring the service
// up via docker compose, run every SQL file under testdata/query/
// through the BigQuery REST query endpoint, and tear down. Asserts
// that each query's response (rendered as headerless-friendly TSV)
// matches the paired golden file.
//
// testdata layout:
//
//	e2e/testdata/
//	├── fixture/                   pre-condition state (empty for now;
//	│                              future seed data / schema YAML can
//	│                              live here and be wired into compose)
//	└── query/
//	    ├── <name>.sql             query to POST
//	    └── <name>.golden.tsv      expected response, line 1 = column
//	                               names (\t-joined), line 2..N = row
//	                               values (\t-joined)
//
// Run from repo root:
//
//	go test -tags=e2e -count=1 ./e2e/...
//
// Optional env:
//
//	REVISION       — REVISION build-arg passed to docker build.
//	                 Defaults to "local-test".
//	READY_TIMEOUT  — seconds to wait for the HTTP port. Default 30.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	composeFile = "compose.yml"
	project     = "test"
	httpAddr    = "http://localhost:9050"
)

// TestSmoke brings up the emulator container once and runs every
// .sql file under testdata/query/ as a sub-test. Each query's
// rendered TSV is compared to the paired .golden.tsv.
func TestSmoke(t *testing.T) {
	readyTimeout := 30 * time.Second
	if v := os.Getenv("READY_TIMEOUT"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("READY_TIMEOUT=%q: %v", v, err)
		}
		readyTimeout = time.Duration(secs) * time.Second
	}

	// Arrange: build, verify --version, bring up, schedule teardown.
	runCompose(t, "build")
	runCompose(t, "run", "--rm", "--no-deps", "bigquery-emulator", "--version")
	runCompose(t, "up", "-d")
	t.Cleanup(func() {
		if err := exec.Command("docker", "compose", "-f", composeFile, "down", "--volumes").Run(); err != nil {
			t.Logf("docker compose down: %v", err)
		}
	})

	waitReady(t, readyTimeout)

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
		goldenPath := strings.TrimSuffix(sqlPath, ".sql") + ".golden.tsv"

		t.Run(name, func(t *testing.T) {
			sqlBytes, err := os.ReadFile(sqlPath)
			if err != nil {
				t.Fatalf("read %s: %v", sqlPath, err)
			}
			sql := strings.TrimSpace(string(sqlBytes))

			wantBytes, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read %s: %v", goldenPath, err)
			}
			want := normaliseTSV(string(wantBytes))

			// Act
			resp := postQuery(t, sql)
			got := normaliseTSV(renderTSV(resp))

			// Assert
			if got != want {
				t.Errorf("response TSV mismatch (%s)\n--- got\n%s\n--- want\n%s", goldenPath, got, want)
			}
		})
	}
}

// queryResponse decodes the synchronous query endpoint shape we
// care about. The full response carries more fields (jobReference,
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

// renderTSV turns a query response into a tab-separated string with
// the column-name header on the first line and row values on the
// subsequent lines. Pairs with normaliseTSV at the comparison site
// so trailing whitespace differences between the response and the
// golden file do not produce noise.
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

// normaliseTSV strips surrounding whitespace and trailing newlines
// so the renderTSV output and the editor-saved golden file compare
// equal even when one ends with an extra newline.
func normaliseTSV(s string) string {
	return strings.TrimRight(s, "\n\r ") + "\n"
}

// runCompose runs `docker compose -f compose.yml <args...>` and
// fails the test on non-zero exit. REVISION flows through so the
// Dockerfile picks it up as a build-arg.
func runCompose(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"compose", "-f", composeFile}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Stdout = testLogWriter{t}
	cmd.Stderr = testLogWriter{t}
	rev := os.Getenv("REVISION")
	if rev == "" {
		rev = "local-test"
	}
	cmd.Env = append(os.Environ(), "REVISION="+rev)
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker compose %s: %v", strings.Join(args, " "), err)
	}
}

// waitReady polls the emulator's HTTP port at 1s intervals until
// any HTTP response is observed (even a 404 is fine — it means the
// router is up).
func waitReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	t.Logf("waiting for %s (timeout=%s)", httpAddr, timeout)
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(httpAddr + "/")
		if err == nil {
			resp.Body.Close()
			t.Log("ready")
			return
		}
		time.Sleep(time.Second)
	}
	dumpLogs(t)
	t.Fatalf("timed out waiting for %s", httpAddr)
}

// postQuery POSTs the SQL to /projects/<project>/queries and
// returns the parsed response. Fails the test on transport, HTTP,
// or JSON-decode errors.
func postQuery(t *testing.T, sql string) queryResponse {
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
	if resp.StatusCode != http.StatusOK {
		dumpLogs(t)
		t.Fatalf("POST %s: HTTP %d, body=%s", url, resp.StatusCode, raw)
	}

	var got queryResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, raw)
	}
	return got
}

// dumpLogs prints the last 50 lines of the emulator's container
// logs to the test output, so a failing run leaves the trace right
// next to the assertion that failed.
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
