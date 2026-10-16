// Package grpc exposes services over gRPC (served by Connect, so the same handler
// also answers gRPC-Web and Connect/JSON). It only translates: proto ↔ model.
// Errors are returned as-is; rpc.HandlerOptions maps apperr sentinels to gRPC codes.
package grpc

import (
	"context"
	"math"

	notev1 "api/gen/go/note/v1"
	"api/gen/go/note/v1/notev1connect"
	"core/apperr"
	"core/components/ginc"
	"core/logger"
	"core/rpc"
	"template/internal/model"
	"template/internal/service"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ notev1connect.NoteServiceHandler = (*NoteHandler)(nil)

type NoteHandler struct {
	svc    *service.NoteService
	logger logger.Logger
}

func NewNoteHandler(svc *service.NoteService, l logger.Logger) *NoteHandler {
	return &NoteHandler{svc: svc, logger: l.With("handler", "note")}
}

// Mount registers the handler on the router under /note.v1.NoteService/.
func (h *NoteHandler) Mount(r gin.IRouter) {
	path, handler := notev1connect.NewNoteServiceHandler(h, rpc.HandlerOptions(h.logger))
	ginc.Mount(r, path, handler)
}

func (h *NoteHandler) CreateNote(ctx context.Context, req *notev1.CreateNoteRequest) (*notev1.CreateNoteResponse, error) {
	note, err := h.svc.Create(ctx, &model.NoteCreate{
		Title:   req.GetTitle(),
		Content: req.GetContent(),
	})
	if err != nil {
		return nil, err
	}
	return &notev1.CreateNoteResponse{Note: toProto(note)}, nil
}

func (h *NoteHandler) GetNote(ctx context.Context, req *notev1.GetNoteRequest) (*notev1.GetNoteResponse, error) {
	id, err := toID(req.GetId())
	if err != nil {
		return nil, err
	}

	note, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &notev1.GetNoteResponse{Note: viewToProto(note)}, nil
}

func (h *NoteHandler) ListNotes(ctx context.Context, req *notev1.ListNotesRequest) (*notev1.ListNotesResponse, error) {
	paging := model.Paging{Page: int(req.GetPage()), Limit: int(req.GetLimit())}

	notes, err := h.svc.List(ctx, &paging)
	if err != nil {
		return nil, err
	}

	res := &notev1.ListNotesResponse{
		Notes: make([]*notev1.Note, 0, len(notes)),
		Page:  int32(paging.Page),
		Limit: int32(paging.Limit),
		Total: paging.Total,
	}
	for i := range notes {
		res.Notes = append(res.Notes, viewToProto(&notes[i]))
	}
	return res, nil
}

func (h *NoteHandler) UpdateNote(ctx context.Context, req *notev1.UpdateNoteRequest) (*notev1.UpdateNoteResponse, error) {
	id, err := toID(req.GetId())
	if err != nil {
		return nil, err
	}

	// proto3 optional fields are generated as pointers: nil means "not sent"
	note, err := h.svc.Update(ctx, id, &model.NoteUpdate{
		Title:   req.Title,
		Content: req.Content,
	})
	if err != nil {
		return nil, err
	}
	return &notev1.UpdateNoteResponse{Note: toProto(note)}, nil
}

func (h *NoteHandler) DeleteNote(ctx context.Context, req *notev1.DeleteNoteRequest) (*notev1.DeleteNoteResponse, error) {
	id, err := toID(req.GetId())
	if err != nil {
		return nil, err
	}

	if err := h.svc.Delete(ctx, id); err != nil {
		return nil, err
	}
	return &notev1.DeleteNoteResponse{}, nil
}

func toProto(n *model.Note) *notev1.Note {
	return &notev1.Note{
		Id:         uint64(n.ID),
		Title:      n.Title,
		Content:    n.Content,
		CreatedBy:  n.CreatedBy,
		CreateTime: timestamppb.New(n.CreatedAt),
		UpdateTime: timestamppb.New(n.UpdatedAt),
	}
}

// viewToProto: a note with the caller's permissions.
func viewToProto(v *model.NoteView) *notev1.Note {
	n := toProto(&v.Note)
	n.Permissions = &notev1.NotePermissions{CanUpdate: v.CanUpdate, CanDelete: v.CanDelete}
	return n
}

func toID(id uint64) (uint, error) {
	if id == 0 || id > math.MaxUint32 {
		return 0, apperr.Invalid("id", "must be a positive integer")
	}
	return uint(id), nil
}
