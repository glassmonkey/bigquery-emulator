DELETE FROM my-dashed-project.ds.t_insert WHERE id = 10;
INSERT INTO my-dashed-project.ds.t_insert (id, val) VALUES (10, 'x');
SELECT id, val FROM my-dashed-project.ds.t_insert ORDER BY id;
