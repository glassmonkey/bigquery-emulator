package server

import (
	"context"

	"github.com/glassmonkey/bigquery-emulator/internal/connection"
	"github.com/glassmonkey/bigquery-emulator/types"
)

func (s *Server) addProjects(ctx context.Context, projects []*types.Project) error {
	for _, project := range projects {
		if err := s.addProject(ctx, project); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) addProject(ctx context.Context, project *types.Project) error {
	conn, err := s.connMgr.Connection(ctx, project.ID, "")
	if err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.RollbackIfNotCommitted()
	for _, dataset := range project.Datasets {
		for _, table := range dataset.Tables {
			table.SetupMetadata(project.ID, dataset.ID)
			if err := s.addTableData(ctx, tx, project, dataset, table); err != nil {
				return err
			}
		}
	}
	p := s.metaRepo.ProjectFromData(project)
	found, err := s.metaRepo.FindProjectWithConn(ctx, tx.Tx(), p.ID)
	if err != nil {
		return err
	}
	if found != nil {
		if err := s.metaRepo.UpdateProject(ctx, tx.Tx(), p); err != nil {
			return err
		}
	} else {
		if err := s.metaRepo.AddProjectIfNotExists(ctx, tx.Tx(), p); err != nil {
			return err
		}
	}
	for _, dataset := range project.Datasets {
		md := s.metaRepo.DatasetFromData(project.ID, dataset)
		if err := s.metaRepo.AddDataset(ctx, tx.Tx(), md); err != nil {
			return err
		}
		for _, table := range dataset.Tables {
			tm := s.metaRepo.TableFromData(project.ID, dataset.ID, table)
			if err := s.metaRepo.AddTable(ctx, tx.Tx(), tm); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Server) addTableData(ctx context.Context, tx *connection.Tx, project *types.Project, dataset *types.Dataset, table *types.Table) error {
	if err := s.contentRepo.CreateOrReplaceTable(ctx, tx, project.ID, dataset.ID, table); err != nil {
		return err
	}
	if err := s.contentRepo.AddTableData(ctx, tx, project.ID, dataset.ID, table); err != nil {
		return err
	}
	return nil
}
