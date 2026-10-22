package repository

import (
	"context"
	"errors"

	"core/apperr"
	"core/tenancy"
	"core/uow"
	"note/internal/model"

	"gorm.io/gorm"
)

// noteRepo isolates tenants twice: every query is filtered by tenancy.Scope, and
// runs through uow.Tx (the service's unit of work, or a short transaction of its
// own) with app.tenant_id set, so the notes table's row-level security policy
// applies too. Another tenant's notes behave as if they did not exist.
type noteRepo struct {
	uow *uow.Gorm
}

func NewNoteRepo(u *uow.Gorm) *noteRepo {
	return &noteRepo{uow: u}
}

// Create expects note.TenantID to be set by the service; RLS (WITH CHECK)
// rejects it if it isn't the caller's tenant.
func (r *noteRepo) Create(ctx context.Context, note *model.Note) error {
	return r.uow.Tx(ctx, func(tx *gorm.DB) error {
		return tx.Create(note).Error
	})
}

func (r *noteRepo) Get(ctx context.Context, id uint) (*model.Note, error) {
	var note model.Note
	err := r.uow.Tx(ctx, func(tx *gorm.DB) error {
		return tx.Scopes(tenancy.Scope(ctx)).Where("id = ?", id).First(&note).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &note, nil
}

func (r *noteRepo) List(ctx context.Context, paging *model.Paging) ([]model.Note, error) {
	notes := []model.Note{}
	err := r.uow.Tx(ctx, func(tx *gorm.DB) error {
		if err := tx.Model(&model.Note{}).Scopes(tenancy.Scope(ctx)).Count(&paging.Total).Error; err != nil {
			return err
		}
		return tx.Scopes(tenancy.Scope(ctx)).
			Order("id DESC").
			Offset((paging.Page - 1) * paging.Limit).
			Limit(paging.Limit).
			Find(&notes).Error
	})
	return notes, err
}

// Update assumes the service already loaded the note (and checked permissions).
func (r *noteRepo) Update(ctx context.Context, id uint, data *model.NoteUpdate) error {
	updates := map[string]interface{}{}
	if data.Title != nil {
		updates["title"] = *data.Title
	}
	if data.Content != nil {
		updates["content"] = *data.Content
	}
	if len(updates) == 0 {
		return nil
	}

	return r.uow.Tx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&model.Note{}).Scopes(tenancy.Scope(ctx)).Where("id = ?", id).Updates(updates).Error
	})
}

func (r *noteRepo) Delete(ctx context.Context, id uint) error {
	var affected int64
	err := r.uow.Tx(ctx, func(tx *gorm.DB) error {
		res := tx.Scopes(tenancy.Scope(ctx)).Where("id = ?", id).Delete(&model.Note{})
		affected = res.RowsAffected
		return res.Error
	})
	if err != nil {
		return err
	}
	if affected == 0 {
		return apperr.ErrNotFound
	}
	return nil
}
