package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"core/apperr"
	"core/authz"
	"core/reqctx"
	"core/tenancy"
	"core/uow"
	"note/internal/model"

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
	rel     authz.Relationships
	uow     uow.UnitOfWork
	created metric.Int64Counter
}

// rel records each note's relationships (tenant, owner) with the embedded ReBAC
// engine, in the same transaction as the note.
func NewNoteService(repo NoteRepository, az authz.Authorizer, rel authz.Relationships, u uow.UnitOfWork) *NoteService {
	// example business metric (Prometheus: notes_created_total); the global meter
	// is set up by core/components/otelc
	created, _ := otel.Meter("note").Int64Counter("notes.created", metric.WithDescription("Notes created"))
	return &NoteService{repo: repo, az: az, rel: rel, uow: u, created: created}
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
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.Create(ctx, note); err != nil {
			return err
		}
		uow.AfterCommit(ctx, func(ctx context.Context) { s.created.Add(ctx, 1) })
		return s.rel.Write(ctx, noteTuples(note)...)
	})
	if err != nil {
		return nil, err
	}
	return note, nil
}

// Get returns a note with the caller's permissions on it.
func (s *NoteService) Get(ctx context.Context, id uint) (*model.NoteView, error) {
	note, err := s.load(ctx, id, authz.Read)
	if err != nil {
		return nil, err
	}
	views, err := s.withPermissions(ctx, []model.Note{*note})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// List returns a page of the tenant's notes with the caller's permissions on
// each, decided in one batch (one BatchCheck call with ReBAC).
func (s *NoteService) List(ctx context.Context, paging *model.Paging) ([]model.NoteView, error) {
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
	notes, err := s.repo.List(ctx, paging)
	if err != nil {
		return nil, err
	}
	return s.withPermissions(ctx, notes)
}

// withPermissions decides update and delete on every note at once. A denial is
// false; any other failure (engine error) fails the call.
func (s *NoteService) withPermissions(ctx context.Context, notes []model.Note) ([]model.NoteView, error) {
	reqs := make([]authz.Request, 0, 2*len(notes))
	for i := range notes {
		res := noteResource(&notes[i])
		reqs = append(reqs, authz.Request{Action: authz.Update, Resource: res}, authz.Request{Action: authz.Delete, Resource: res})
	}
	errs := authz.CheckMany(ctx, s.az, reqs)

	views := make([]model.NoteView, len(notes))
	for i := range notes {
		canUpdate, err := granted(errs[2*i])
		if err != nil {
			return nil, err
		}
		canDelete, err := granted(errs[2*i+1])
		if err != nil {
			return nil, err
		}
		views[i] = model.NoteView{Note: notes[i], CanUpdate: canUpdate, CanDelete: canDelete}
	}
	return views, nil
}

// granted: nil → true, a denial → false, anything else is returned.
func granted(err error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, apperr.ErrForbidden), errors.Is(err, apperr.ErrNotFound):
		return false, nil
	}
	return false, err
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
		note, err := s.load(ctx, id, authz.Delete)
		if err != nil {
			return err
		}
		if err := s.repo.Delete(ctx, id); err != nil {
			return err
		}
		// owner, tenant and any share of the note
		return s.rel.DeleteObjects(ctx, noteObject(note))
	})
}

func noteObject(n *model.Note) string {
	return ResourceNote + ":" + strconv.FormatUint(uint64(n.ID), 10)
}

// noteTuples: a note belongs to its tenant and is owned by its creator
// (relations "tenant" and "owner" of type note in authz/authz.yml).
func noteTuples(n *model.Note) []authz.Tuple {
	object := noteObject(n)
	return []authz.Tuple{
		{Object: object, Relation: "tenant", Subject: "tenant:" + n.TenantID, TenantID: n.TenantID},
		{Object: object, Relation: "owner", Subject: "user:" + n.CreatedBy, TenantID: n.TenantID},
	}
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
