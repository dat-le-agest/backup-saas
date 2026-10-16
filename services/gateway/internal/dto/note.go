// Package dto holds the public JSON shapes of the gateway and their mapping
// to/from upstream proto messages. Proto types never reach the client directly,
// so the public API stays stable when internal contracts evolve.
package dto

import (
	"time"

	notev1 "api/gen/go/note/v1"
)

type Note struct {
	ID        uint64    `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateNoteRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// UpdateNoteRequest: omitted fields are left unchanged.
type UpdateNoteRequest struct {
	Title   *string `json:"title"`
	Content *string `json:"content"`
}

type ListNotesQuery struct {
	Page  int32 `form:"page"`
	Limit int32 `form:"limit"`
}

type Paging struct {
	Page  int32 `json:"page"`
	Limit int32 `json:"limit"`
	Total int64 `json:"total"`
}

func NoteFromProto(n *notev1.Note) Note {
	return Note{
		ID:        n.GetId(),
		Title:     n.GetTitle(),
		Content:   n.GetContent(),
		CreatedBy: n.GetCreatedBy(),
		CreatedAt: n.GetCreateTime().AsTime(),
		UpdatedAt: n.GetUpdateTime().AsTime(),
	}
}

func NotesFromProto(notes []*notev1.Note) []Note {
	out := make([]Note, 0, len(notes))
	for _, n := range notes {
		out = append(out, NoteFromProto(n))
	}
	return out
}

func (r CreateNoteRequest) ToProto() *notev1.CreateNoteRequest {
	return &notev1.CreateNoteRequest{Title: r.Title, Content: r.Content}
}

func (r UpdateNoteRequest) ToProto(id uint64) *notev1.UpdateNoteRequest {
	return &notev1.UpdateNoteRequest{Id: id, Title: r.Title, Content: r.Content}
}

func (q ListNotesQuery) ToProto() *notev1.ListNotesRequest {
	return &notev1.ListNotesRequest{Page: q.Page, Limit: q.Limit}
}

func PagingFromProto(res *notev1.ListNotesResponse) Paging {
	return Paging{Page: res.GetPage(), Limit: res.GetLimit(), Total: res.GetTotal()}
}
