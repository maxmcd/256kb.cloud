// Code generated by sqlc. DO NOT EDIT.
// versions:
//   sqlc v1.18.0

package db

import (
	"context"
)

type Querier interface {
	NewBuild(ctx context.Context, command string) (Build, error)
	SelectBuild(ctx context.Context, id int64) (Build, error)
}

var _ Querier = (*Queries)(nil)
