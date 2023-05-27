-- name: SelectBuild :one
select *
from build
where id = ?;
-- name: NewBuild :one
INSERT INTO build (command)
VALUES (?)
RETURNING *;
