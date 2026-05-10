// Smoke tests the bigquery-emulator container image end to end.
//
// Builds the image from the top-level Dockerfile, verifies that the
// binary starts (`--version` inside the image), brings the service
// up via docker compose, polls for readiness, runs SELECT 1 against
// the BigQuery REST API, and tears the service down. Returns
// non-zero on any failure and dumps the recent emulator logs.
//
// Usage (from repo root):
//
//	go run ./e2e/cmd/smoke
//
// Flags:
//
//	-compose-file   path to compose.yml (default: e2e/compose.yml)
//	-project        BigQuery project ID for the emulator (default: test)
//	-addr           emulator REST endpoint (default: http://localhost:9050)
//	-ready-timeout  max wait for the HTTP port to come up (default: 30s)
//	-revision       REVISION build-arg for docker build
//	                (default: $REVISION env var)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	composeFile := flag.String("compose-file", "e2e/compose.yml", "path to compose.yml")
	project := flag.String("project", "test", "BigQuery project ID for the emulator")
	addr := flag.String("addr", "http://localhost:9050", "emulator REST endpoint")
	readyTimeout := flag.Duration("ready-timeout", 30*time.Second, "max wait for the HTTP port to come up")
	revision := flag.String("revision", os.Getenv("REVISION"), "REVISION build-arg for docker build")
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("==> ")

	r := &runner{
		composeFile:  *composeFile,
		revision:     *revision,
		project:      *project,
		addr:         strings.TrimRight(*addr, "/"),
		readyTimeout: *readyTimeout,
	}
	if err := r.run(); err != nil {
		log.Println("FAIL:", err)
		log.Println("recent emulator logs:")
		_ = r.compose("logs", "--tail=50", "bigquery-emulator")
		os.Exit(1)
	}
}

type runner struct {
	composeFile  string
	revision     string
	project      string
	addr         string
	readyTimeout time.Duration
}

func (r *runner) run() error {
	log.Printf("building image (REVISION=%q)", r.revision)
	if err := r.compose("build"); err != nil {
		return fmt.Errorf("docker compose build: %w", err)
	}

	log.Println("verifying --version inside the image")
	if err := r.compose("run", "--rm", "--no-deps", "bigquery-emulator", "--version"); err != nil {
		return fmt.Errorf("--version: %w", err)
	}

	log.Println("starting emulator")
	if err := r.compose("up", "-d"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	defer func() {
		log.Println("stopping emulator")
		_ = r.compose("down", "--volumes")
	}()

	if err := r.waitReady(); err != nil {
		return err
	}
	log.Println("ready")

	log.Printf("running SELECT 1 against %s/projects/%s/queries", r.addr, r.project)
	if err := r.selectOne(); err != nil {
		return err
	}
	log.Println("SELECT 1 returned the expected row")
	log.Println("SMOKE TEST PASSED")
	return nil
}

// compose runs `docker compose -f <file> <args...>`, propagating
// REVISION through the environment so the Dockerfile picks it up as
// a build-arg.
func (r *runner) compose(args ...string) error {
	full := append([]string{"compose", "-f", r.composeFile}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if r.revision != "" {
		cmd.Env = append(os.Environ(), "REVISION="+r.revision)
	}
	return cmd.Run()
}

// waitReady polls the emulator's HTTP port at 1s intervals until any
// HTTP response is observed (even a 404 is fine — it means the
// router is up). Times out at r.readyTimeout.
func (r *runner) waitReady() error {
	log.Printf("waiting for %s (timeout=%s)", r.addr, r.readyTimeout)
	deadline := time.Now().Add(r.readyTimeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(r.addr + "/")
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("timed out waiting for %s", r.addr)
}

// selectOne POSTs SELECT 1 to /projects/<project>/queries and
// asserts that rows[0].f[0].v == "1". The synchronous query
// endpoint runs the SQL through the parser, analyzer, formatter,
// SQLite executor, and result serializer in one round-trip, so a
// passing assert means the whole pipeline is wired up.
func (r *runner) selectOne() error {
	url := fmt.Sprintf("%s/projects/%s/queries", r.addr, r.project)
	body := strings.NewReader(`{"query":"SELECT 1 AS one","useLegacySql":false}`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	fmt.Println(string(bytes.TrimSpace(raw)))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var got struct {
		Rows []struct {
			F []struct {
				V string `json:"v"`
			} `json:"f"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(got.Rows) == 0 || len(got.Rows[0].F) == 0 {
		return fmt.Errorf("response carried no rows")
	}
	if v := got.Rows[0].F[0].V; v != "1" {
		return fmt.Errorf("rows[0].f[0].v = %q, want %q", v, "1")
	}
	return nil
}
