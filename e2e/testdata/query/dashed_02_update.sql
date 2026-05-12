UPDATE my-dashed-project.ds.t_update SET val = 'original' WHERE id = 1;
UPDATE my-dashed-project.ds.t_update SET val = 'updated' WHERE id = 1;
SELECT id, val FROM my-dashed-project.ds.t_update ORDER BY id;
