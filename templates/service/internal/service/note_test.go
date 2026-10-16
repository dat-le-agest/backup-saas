package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"core/apperr"
	"core/authz"
	"core/reqctx"
	"core/uow"
	"template/internal/model"
)

// fakeRepo stores notes in memory and records writes. Tenant scoping is the real
// repository's job (tested end-to-end); here we only test the service's decisions.
type fakeRepo struct {
	notes   map[uint]*model.Note
	created *model.Note
	paging  *model.Paging
	updated *model.NoteUpdate
	deleted []uint
}

func newRepo(notes ...*model.Note) *fakeRepo {
	r := &fakeRepo{notes: map[uint]*model.Note{}}
	for _, n := range notes {
		r.notes[n.ID] = n
	}
	return r
}

func (f *fakeRepo) Create(_ context.Context, n *model.Note) error { f.created = n; return nil }
func (f *fakeRepo) Get(_ context.Context, id uint) (*model.Note, error) {
	if n, ok := f.notes[id]; ok {
		return n, nil
	}
	return nil, apperr.ErrNotFound
}
func (f *fakeRepo) List(_ context.Context, p *model.Paging) ([]model.Note, error) {
	f.paging = p
	return nil, nil
}
func (f *fakeRepo) Update(_ context.Context, _ uint, d *model.NoteUpdate) error {
	f.updated = d
	return nil
}
func (f *fakeRepo) Delete(_ context.Context, id uint) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func as(userID, role string) context.Context {
	return reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: userID, TenantID: "t1", Role: role})
}

func newSvc(repo *fakeRepo) *NoteService {
	return NewNoteService(repo, authz.NewRBAC(NotePolicy), uow.Nop{})
}

func TestCreateStampsTenantAndOwner(t *testing.T) {
	repo := newRepo()
	if _, err := newSvc(repo).Create(as("u1", authz.RoleMember), &model.NoteCreate{Title: "  hello "}); err != nil {
		t.Fatal(err)
	}
	if n := repo.created; n.TenantID != "t1" || n.CreatedBy != "u1" || n.Title != "hello" {
		t.Fatalf("created: %+v", n)
	}
}

func TestCreateRequiresIdentityAndPermission(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"anonymous", context.Background(), apperr.ErrUnauthenticated},
		{"no tenant", reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u1", Role: authz.RoleAdmin}), apperr.ErrForbidden},
		{"viewer", as("u1", authz.RoleViewer), apperr.ErrForbidden},
	}
	for _, tc := range cases {
		repo := newRepo()
		// empty title: permission must be checked before validation
		if _, err := newSvc(repo).Create(tc.ctx, &model.NoteCreate{Title: ""}); !errors.Is(err, tc.want) {
			t.Errorf("%s: expected %v, got %v", tc.name, tc.want, err)
		}
		if repo.created != nil {
			t.Errorf("%s: note was created", tc.name)
		}
	}
}

func TestOwnershipRules(t *testing.T) {
	mine := &model.Note{ID: 1, TenantID: "t1", CreatedBy: "u1"}
	theirs := &model.Note{ID: 2, TenantID: "t1", CreatedBy: "u2"}
	content := "x"

	cases := []struct {
		name    string
		ctx     context.Context
		id      uint
		allowed bool
	}{
		{"member own", as("u1", authz.RoleMember), mine.ID, true},
		{"member others", as("u1", authz.RoleMember), theirs.ID, false},
		{"admin others", as("u1", authz.RoleAdmin), theirs.ID, true},
		{"owner others", as("u1", authz.RoleOwner), theirs.ID, true},
		{"viewer own", as("u1", authz.RoleViewer), mine.ID, false},
	}
	for _, tc := range cases {
		repo := newRepo(mine, theirs)
		svc := newSvc(repo)

		_, uerr := svc.Update(tc.ctx, tc.id, &model.NoteUpdate{Content: &content})
		derr := svc.Delete(tc.ctx, tc.id)

		if tc.allowed {
			if uerr != nil || derr != nil || len(repo.deleted) != 1 {
				t.Errorf("%s: expected allowed, got update=%v delete=%v", tc.name, uerr, derr)
			}
			continue
		}
		if !errors.Is(uerr, apperr.ErrForbidden) || !errors.Is(derr, apperr.ErrForbidden) {
			t.Errorf("%s: expected forbidden, got update=%v delete=%v", tc.name, uerr, derr)
		}
		if repo.updated != nil || len(repo.deleted) != 0 {
			t.Errorf("%s: write reached the repository", tc.name)
		}
	}
}

func TestEveryRoleCanRead(t *testing.T) {
	theirs := &model.Note{ID: 2, TenantID: "t1", CreatedBy: "u2"}
	for _, role := range authz.Roles {
		svc := newSvc(newRepo(theirs))
		if _, err := svc.Get(as("u1", role), theirs.ID); err != nil {
			t.Errorf("%s get: %v", role, err)
		}
		if _, err := svc.List(as("u1", role), &model.Paging{}); err != nil {
			t.Errorf("%s list: %v", role, err)
		}
	}
	if _, err := newSvc(newRepo()).List(as("u1", "intruder"), &model.Paging{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("unknown role list: %v", err)
	}
}

func TestCreateValidatesTitle(t *testing.T) {
	svc := newSvc(newRepo())
	for _, title := range []string{"", "   ", strings.Repeat("a", 256)} {
		if _, err := svc.Create(as("u1", authz.RoleMember), &model.NoteCreate{Title: title}); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("title %q: expected apperr.ErrInvalid, got %v", title, err)
		}
	}
}

func TestUpdateValidatesTitleOnlyWhenPresent(t *testing.T) {
	mine := &model.Note{ID: 1, TenantID: "t1", CreatedBy: "u1"}
	svc := newSvc(newRepo(mine))

	empty := " "
	if _, err := svc.Update(as("u1", authz.RoleMember), 1, &model.NoteUpdate{Title: &empty}); !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("expected apperr.ErrInvalid, got %v", err)
	}
	content := "body"
	if _, err := svc.Update(as("u1", authz.RoleMember), 1, &model.NoteUpdate{Content: &content}); err != nil {
		t.Fatal(err)
	}
}

func TestListClampsPaging(t *testing.T) {
	cases := []struct{ page, limit, wantPage, wantLimit int }{
		{0, 0, 1, defaultLimit},
		{-3, 1000, 1, maxLimit},
		{2, 5, 2, 5},
	}
	for _, tc := range cases {
		repo := newRepo()
		_, _ = newSvc(repo).List(as("u1", authz.RoleViewer), &model.Paging{Page: tc.page, Limit: tc.limit})
		if repo.paging.Page != tc.wantPage || repo.paging.Limit != tc.wantLimit {
			t.Errorf("in (%d,%d): got (%d,%d)", tc.page, tc.limit, repo.paging.Page, repo.paging.Limit)
		}
	}
}
