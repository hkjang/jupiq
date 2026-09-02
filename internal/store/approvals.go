package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// TransitionResource atomically moves a resource out of the expected state.
// The returned boolean is false when another request won the transition.
func (s *Store) TransitionResource(ctx context.Context, kind string, id int64, expectedStatus string, input ResourceWrite, actorID int64) (Resource, bool, error) {
	if input.Status == "" {
		input.Status = "active"
	}
	if len(input.Data) == 0 {
		input.Data = json.RawMessage(`{}`)
	}
	var result Resource
	var raw []byte
	err := s.Pool.QueryRow(ctx, `
		UPDATE resources
		SET name=$5,status=$4,owner_user_id=$6,data=$7,updated_by=$8,updated_at=now()
		WHERE kind=$1 AND id=$2 AND status=$3
		RETURNING id,kind,name,status,owner_user_id,data,created_by,updated_by,created_at,updated_at`,
		kind, id, expectedStatus, input.Status, input.Name, input.OwnerUserID, input.Data, actorID,
	).Scan(&result.ID, &result.Kind, &result.Name, &result.Status, &result.OwnerUserID, &raw, &result.CreatedBy, &result.UpdatedBy, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Resource{}, false, nil
		}
		return Resource{}, false, err
	}
	result.Data = json.RawMessage(raw)
	return result, true, nil
}
