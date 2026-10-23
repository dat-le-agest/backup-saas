// Package store keeps the admin accounts in Postgres.
package store

import (
	"context"
	"errors"
	"time"

	"admin/internal/model"
	"core/apperr"

	"gorm.io/gorm"
)

type Store struct{ db *gorm.DB }

func New(db *gorm.DB) *Store { return &Store{db: db} }

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.ErrNotFound
	}
	return err
}

func (s *Store) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.Admin{}).Count(&n).Error
	return n, err
}

// Create returns apperr.ErrConflict if the email is taken.
func (s *Store) Create(ctx context.Context, a *model.Admin) error {
	err := s.db.WithContext(ctx).Create(a).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return apperr.ErrConflict
	}
	return err
}

func (s *Store) ByEmail(ctx context.Context, email string) (*model.Admin, error) {
	var a model.Admin
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&a).Error; err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

func (s *Store) ByID(ctx context.Context, id string) (*model.Admin, error) {
	var a model.Admin
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&a).Error; err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

func (s *Store) List(ctx context.Context) ([]model.Admin, error) {
	var out []model.Admin
	err := s.db.WithContext(ctx).Order("created_at").Find(&out).Error
	return out, err
}

func (s *Store) TouchLogin(ctx context.Context, id string, at time.Time) error {
	return s.db.WithContext(ctx).Model(&model.Admin{}).Where("id = ?", id).Update("last_login_at", at).Error
}
