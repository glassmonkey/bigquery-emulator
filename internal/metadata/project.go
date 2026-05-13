package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Project bundles a BigQuery project's identity together with its
// datasets, which are kept in memory and synced through the
// underlying SQL store. Jobs are deliberately *not* held on the
// Project struct: they live in the `jobs` table keyed by projectID
// and are fetched on demand. Materialising the job list on every
// project lookup made `withProjectMiddleware` cost O(N) per request
// in the number of completed jobs — see issue #90.
type Project struct {
	ID         string
	datasets   []*Dataset
	datasetMap map[string]*Dataset
	mu         sync.RWMutex
	repo       *Repository
}

func (p *Project) DatasetIDs() []string {
	ids := make([]string, len(p.datasets))
	for i := 0; i < len(p.datasets); i++ {
		ids[i] = p.datasets[i].ID
	}
	return ids
}

// Job resolves a single job by ID through the repository. The lookup
// is a primary-key seek on `jobs(projectID, id)` so it stays O(1)
// regardless of how many jobs the project has accumulated.
func (p *Project) Job(ctx context.Context, id string) (*Job, error) {
	return p.repo.FindJob(ctx, p.ID, id)
}

// Jobs returns every job belonging to this project. The cost is
// O(M) in the project's own job count, not in the cluster-wide
// total — the index on `jobs(projectID, id)` confines the scan.
func (p *Project) Jobs(ctx context.Context) ([]*Job, error) {
	return p.repo.FindJobsByProject(ctx, p.ID)
}

func (p *Project) Dataset(id string) *Dataset {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.datasetMap[id]
}

func (p *Project) Datasets() []*Dataset {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.datasets
}

func (p *Project) Insert(ctx context.Context, tx *sql.Tx) error {
	return p.repo.AddProject(ctx, tx, p)
}

func (p *Project) Delete(ctx context.Context, tx *sql.Tx) error {
	return p.repo.DeleteProject(ctx, tx, p)
}

func (p *Project) AddDataset(ctx context.Context, tx *sql.Tx, dataset *Dataset) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.datasetMap[dataset.ID]; exists {
		return fmt.Errorf("dataset %s is already created", dataset.ID)
	}
	if err := dataset.Insert(ctx, tx); err != nil {
		return err
	}
	p.datasets = append(p.datasets, dataset)
	p.datasetMap[dataset.ID] = dataset
	if err := p.repo.UpdateProject(ctx, tx, p); err != nil {
		return err
	}
	return nil
}

func (p *Project) DeleteDataset(ctx context.Context, tx *sql.Tx, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	dataset, exists := p.datasetMap[id]
	if !exists {
		return fmt.Errorf("dataset '%s' is not found in project '%s'", id, p.ID)
	}
	if err := dataset.Delete(ctx, tx); err != nil {
		return err
	}
	newDatasets := make([]*Dataset, 0, len(p.datasets))
	for _, dataset := range p.datasets {
		if dataset.ID == id {
			continue
		}
		newDatasets = append(newDatasets, dataset)
	}
	p.datasets = newDatasets
	delete(p.datasetMap, id)
	if err := p.repo.UpdateProject(ctx, tx, p); err != nil {
		return err
	}
	return nil
}

// AddJob writes a new job for this project to the `jobs` table.
// Unlike before, no in-memory cache is updated and the parent
// `projects` row is not touched — the job's `projectID` column
// is enough to put it in this project's bucket.
func (p *Project) AddJob(ctx context.Context, tx *sql.Tx, job *Job) error {
	return job.Insert(ctx, tx)
}

// DeleteJob removes a job from the `jobs` table. Same shape as
// AddJob: no parent-row update, no in-memory state to keep in
// sync.
func (p *Project) DeleteJob(ctx context.Context, tx *sql.Tx, id string) error {
	job, err := p.repo.FindJobWithConn(ctx, tx, p.ID, id)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("job '%s' is not found in project '%s'", id, p.ID)
	}
	return job.Delete(ctx, tx)
}

func NewProject(repo *Repository, id string, datasets []*Dataset) *Project {
	datasetMap := map[string]*Dataset{}
	for _, dataset := range datasets {
		datasetMap[dataset.ID] = dataset
	}
	return &Project{
		ID:         id,
		datasets:   datasets,
		datasetMap: datasetMap,
		repo:       repo,
	}
}
