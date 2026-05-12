DELETE FROM my-dashed-project.ds.t_delete WHERE id IN (1, 2);
INSERT INTO my-dashed-project.ds.t_delete (id, val) VALUES (1, 'a'), (2, 'b');
DELETE FROM my-dashed-project.ds.t_delete WHERE id = 2;
SELECT id, val FROM my-dashed-project.ds.t_delete ORDER BY id;
