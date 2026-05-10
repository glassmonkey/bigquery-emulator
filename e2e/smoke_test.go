//go:build e2e

// Smoke tests the bigquery-emulator container image end to end.
// Build the image from the top-level Dockerfile, bring the service
// up via docker compose, run every SQL file under testdata/ through
// the BigQuery REST query endpoint, and tear down. Asserts that
// each query returns the expected first-column value.
//
// Run from repo root:
//
//	go test -tags=e2e -count=1 ./e2e/...
//
// The `e2e` build tag keeps `go test ./...` from spinning up Docker
// inadvertently. -count=1 disables go test's pass-cache so the
// harness runs against a freshly built image each time.
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

// TestSmoke brings up the emulator container once and runs each
// testdata case through the synchronous BigQuery REST query
// endpoint. Cases are sub-tests so a regression in one query type
// shows up clearly under `TestSmoke/<name>`.
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

	for _, tc := range []struct {
		name    string
		sqlFile string
		want    string // expected rows[0].f[0].v
	}{
		{
			name:    "select_one",
			sqlFile: "testdata/select_one.sql",
			want:    "1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlBytes, err := os.ReadFile(tc.sqlFile)
			if err != nil {
				t.Fatalf("read %s: %v", tc.sqlFile, err)
			}
			sql := strings.TrimSpace(string(sqlBytes))

			// Act
			got := postQuery(t, sql)

			// Assert
			if len(got.Rows) == 0 || len(got.Rows[0].F) == 0 {
				t.Fatalf("response carried no rows: %+v", got)
			}
			if v := got.Rows[0].F[0].V; v != tc.want {
				t.Errorf("rows[0].f[0].v = %q, want %q", v, tc.want)
			}
		})
	}
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
	if rev := os.Getenv("REVISION"); rev != "" {
		cmd.Env = append(os.Environ(), "REVISION="+rev)
	} else {
		cmd.Env = append(os.Environ(), "REVISION=local-test")
	}
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

// queryResponse decodes the synchronous query endpoint shape we
// care about. The full response carries more fields (jobReference,
// schema, totalRows, ...) but the smoke harness only needs the
// first row's first column.
type queryResponse struct {
	Rows []struct {
		F []struct {
			V string `json:"v"`
		} `json:"f"`
	} `json:"rows"`
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
