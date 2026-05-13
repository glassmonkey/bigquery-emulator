package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	internaltypes "github.com/glassmonkey/bigquery-emulator/internal/types"
	bigqueryv2 "google.golang.org/api/bigquery/v2"
)

type Job struct {
	ID        string
	ProjectID string
	content   *bigqueryv2.Job
	response  *internaltypes.QueryResponse
	err       error
	repo      *Repository
}

func (j *Job) Query() string {
	return j.content.Configuration.Query.Query
}

func (j *Job) QueryParameters() []*bigqueryv2.QueryParameter {
	return j.content.Configuration.Query.QueryParameters
}

func (j *Job) SetResult(ctx context.Context, tx *sql.Tx, response *internaltypes.QueryResponse, err error) error {
	j.response = response
	j.err = err
	if err := j.repo.UpdateJob(ctx, tx, j); err != nil {
		return fmt.Errorf("failed to update job: %w", err)
	}
	return nil
}

func (j *Job) Content() *bigqueryv2.Job {
	return j.content
}

// Wait blocks until the job's result row is visible in the
// `jobs` table and returns the stored response. Reads-only on
// the receiver, so it intentionally does not take j.mu — the
// loop body only touches the repository.
//
// Fast path: jobsInsertHandler runs the query synchronously and
// commits the result row before its HTTP response returns, so by
// the time an SDK client starts polling `getQueryResults` the
// row is almost always already there. The probe up front avoids
// the 100 ms wasted on the first ticker tick (issue #94).
func (j *Job) Wait(ctx context.Context) (*internaltypes.QueryResponse, error) {
	if foundJob, err := j.repo.FindJob(ctx, j.ProjectID, j.ID); err != nil {
		return nil, err
	} else if foundJob != nil {
		return foundJob.response, foundJob.err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			foundJob, err := j.repo.FindJob(ctx, j.ProjectID, j.ID)
			if err != nil {
				return nil, err
			}
			if foundJob != nil {
				return foundJob.response, foundJob.err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (j *Job) Cancel(ctx context.Context) error {
	// TODO: job needs to be able to rollback
	return nil
}

func (j *Job) Insert(ctx context.Context, tx *sql.Tx) error {
	return j.repo.AddJob(ctx, tx, j)
}

func (j *Job) Delete(ctx context.Context, tx *sql.Tx) error {
	return j.repo.DeleteJob(ctx, tx, j)
}

func NewJob(repo *Repository, projectID, jobID string, content *bigqueryv2.Job, response *internaltypes.QueryResponse, err error) *Job {
	return &Job{
		ID:        jobID,
		ProjectID: projectID,
		content:   content,
		response:  response,
		err:       err,
		repo:      repo,
	}
}
