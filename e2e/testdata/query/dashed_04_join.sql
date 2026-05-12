SELECT a.id, b.val
FROM my-dashed-project.ds.t AS a
JOIN my-dashed-project.ds.src AS b
ON a.id = b.id
ORDER BY a.id
