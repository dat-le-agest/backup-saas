package repository

import (
	"context"
	"errors"
	"time"

	"core/apperr"

	"gorm.io/gorm"
)

// StoredModel is a row of authorization_models.
type StoredModel struct {
	ID            string
	SchemaVersion string
	DSL           string
	Model         []byte // protojson of openfgav1.AuthorizationModel
	IsActive      bool
	CreatedBy     string
	CreatedAt     time.Time
}

func (s *Store) CreateModel(ctx context.Context, m *StoredModel) error {
	return s.db(ctx).Exec(`
		INSERT INTO `+s.t("authorization_models")+` (id, schema_version, dsl, model, created_by)
		VALUES (?, ?, ?, ?::jsonb, ?)`,
		m.ID, m.SchemaVersion, m.DSL, string(m.Model), m.CreatedBy,
	).Error
}

// GetModel returns a model by id, or the active one when id is "".
func (s *Store) GetModel(ctx context.Context, id string) (*StoredModel, error) {
	var m StoredModel
	q := s.db(ctx).Table(s.table("authorization_models")).
		Select("id, schema_version, dsl, model::text AS model, is_active, created_by, created_at")
	if id == "" {
		q = q.Where("is_active")
	} else {
		q = q.Where("id = ?", id)
	}
	if err := q.Take(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if id == "" {
				return nil, apperr.Invalid("authorization_model_id", "no active authorization model")
			}
			return nil, apperr.ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// ActiveModelID returns the id of the active model, "" if none.
func (s *Store) ActiveModelID(ctx context.Context) (string, error) {
	var id string
	err := s.db(ctx).Raw(`SELECT coalesce((SELECT id FROM ` + s.t("authorization_models") + ` WHERE is_active), '')`).Scan(&id).Error
	return id, err
}

// Activate makes id the only active model. Call inside a unit of work.
func (s *Store) Activate(ctx context.Context, id string) error {
	if err := s.db(ctx).Exec(`UPDATE `+s.t("authorization_models")+` SET is_active = false WHERE is_active AND id <> ?`, id).Error; err != nil {
		return err
	}
	res := s.db(ctx).Exec(`UPDATE `+s.t("authorization_models")+` SET is_active = true WHERE id = ?`, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apperr.ErrNotFound
	}
	return nil
}
