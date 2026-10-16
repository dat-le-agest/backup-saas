package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"core/apperr"
	"core/authz"
	"core/reqctx"
	"core/tenancy"
	"core/uow"
	"template/internal/model"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const (
	defaultLimit  = 20
	maxLimit      = 100
	maxTitleRunes = 255
)

// NoteRepository is defined by its consumer (the service), implemented in internal/repository.
// Implementations scope every query to the caller's tenant (core/tenancy) and
// return apperr sentinels (e.g. apperr.ErrNotFound) for expected failures.
type NoteRepository interface {
	Create(ctx context.Context, note *model.Note) error
	Get(ctx context.Context, id uint) (*model.Note, error)
	List(ctx context.Context, paging *model.Paging) ([]model.Note, error)
	Update(ctx context.Context, id uint, data *model.NoteUpdate) error
	Delete(ctx context.Context, id uint) error
}

// NoteService checks permissions (authz) on every operation; tenant isolation is
// enforced independently by the repository. Use cases that read then write run
// in one unit of work, so the permission check and the write see the same row.
type NoteService struct {
	repo    NoteRepository
	az      authz.Authorizer
	uow     uow.UnitOfWork
	created metric.Int64Counter
}

func NewNoteService(repo NoteRepository, az authz.Authorizer, u uow.UnitOfWork) *NoteService {
	// example business metric (Prometheus: notes_created_total); the global meter
	// is set up by core/components/otelc
	created, _ := otel.Meter("note").Int64Counter("notes.created", metric.WithDescription("Notes created"))
	return &NoteService{repo: repo, az: az, uow: u, created: created}
}

func (s *NoteService) Create(ctx context.Context, data *model.NoteCreate) (*model.Note, error) {
	tenantID, err := tenancy.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	id, _ := reqctx.IdentityFrom(ctx)

	// the new note will be owned by the caller
	if err := s.az.Check(ctx, authz.Create, authz.Resource{Type: ResourceNote, TenantID: tenantID, OwnerID: id.UserID}); err != nil {
		return nil, err
	}

	title, err := validateTitle(data.Title)
	if err != nil {
		return nil, err
	}

	note := &model.Note{TenantID: tenantID, CreatedBy: id.UserID, Title: title, Content: data.Content}
	if err := s.repo.Create(ctx, note); err != nil {
		return nil, err
	}
	uow.AfterCommit(ctx, func(ctx context.Context) { s.created.Add(ctx, 1) })
	return note, nil
}

func (s *NoteService) Get(ctx context.Context, id uint) (*model.Note, error) {
	return s.load(ctx, id, authz.Read)
}

func (s *NoteService) List(ctx context.Context, paging *model.Paging) ([]model.Note, error) {
	tenantID, err := tenancy.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	// type-level check: listing needs read on any note of the tenant
	if err := s.az.Check(ctx, authz.Read, authz.Resource{Type: ResourceNote, TenantID: tenantID}); err != nil {
		return nil, err
	}

	if paging.Page < 1 {
		paging.Page = 1
	}
	if paging.Limit < 1 {
		paging.Limit = defaultLimit
	}
	if paging.Limit > maxLimit {
		paging.Limit = maxLimit
	}
	return s.repo.List(ctx, paging)
}

func (s *NoteService) Update(ctx context.Context, id uint, data *model.NoteUpdate) (*model.Note, error) {
	var updated *model.Note
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if _, err := s.load(ctx, id, authz.Update); err != nil {
			return err
		}

		if data.Title != nil {
			title, err := validateTitle(*data.Title)
			if err != nil {
				return err
			}
			data.Title = &title
		}

		if err := s.repo.Update(ctx, id, data); err != nil {
			return err
		}
		var err error
		updated, err = s.repo.Get(ctx, id)
		return err
	})
	return updated, err
}

func (s *NoteService) Delete(ctx context.Context, id uint) error {
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if _, err := s.load(ctx, id, authz.Delete); err != nil {
			return err
		}
		return s.repo.Delete(ctx, id)
	})
}

// load fetches a note (tenant-scoped by the repository) and checks action on it.
func (s *NoteService) load(ctx context.Context, id uint, action string) (*model.Note, error) {
	note, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.az.Check(ctx, action, noteResource(note)); err != nil {
		return nil, err
	}
	return note, nil
}

func noteResource(n *model.Note) authz.Resource {
	return authz.Resource{
		Type:     ResourceNote,
		ID:       strconv.FormatUint(uint64(n.ID), 10),
		TenantID: n.TenantID,
		OwnerID:  n.CreatedBy,
	}
}

func validateTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", apperr.Invalid("title", "is required")
	}
	if len([]rune(title)) > maxTitleRunes {
		return "", apperr.Invalid("title", fmt.Sprintf("must be at most %d characters", maxTitleRunes))
	}
	return title, nil
}
