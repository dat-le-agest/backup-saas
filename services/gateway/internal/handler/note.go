package handler

import (
	"fmt"
	"net/http"
	"strconv"

	notev1 "api/gen/go/note/v1"
	"api/gen/go/note/v1/notev1connect"
	"core/apperr"
	"core/response"
	"gateway/internal/dto"

	"github.com/gin-gonic/gin"
)

// NoteHandler maps the public REST API to the note service's gRPC contract.
// Business validation (e.g. title rules) stays in the note service; errors it
// returns arrive here as apperr sentinels and keep their status and message.
type NoteHandler struct {
	notes notev1connect.NoteServiceClient
}

func NewNoteHandler(notes notev1connect.NoteServiceClient) *NoteHandler {
	return &NoteHandler{notes: notes}
}

func (h *NoteHandler) RegisterRoutes(rg *gin.RouterGroup) {
	notes := rg.Group("/notes")
	notes.POST("", h.create)
	notes.GET("", h.list)
	notes.GET("/:id", h.get)
	notes.PATCH("/:id", h.update)
	notes.DELETE("/:id", h.delete)
}

func (h *NoteHandler) create(c *gin.Context) {
	var body dto.CreateNoteRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, errInvalidBody)
		return
	}

	res, err := h.notes.CreateNote(c.Request.Context(), body.ToProto())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusCreated, dto.NoteFromProto(res.GetNote()))
}

func (h *NoteHandler) list(c *gin.Context) {
	var query dto.ListNotesQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Fail(c, fmt.Errorf("%w: page and limit must be integers", apperr.ErrInvalid))
		return
	}

	res, err := h.notes.ListNotes(c.Request.Context(), query.ToProto())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.List(c, dto.NotesFromProto(res.GetNotes()), dto.PagingFromProto(res))
}

func (h *NoteHandler) get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}

	res, err := h.notes.GetNote(c.Request.Context(), &notev1.GetNoteRequest{Id: id})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.NoteFromProto(res.GetNote()))
}

func (h *NoteHandler) update(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}

	var body dto.UpdateNoteRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, errInvalidBody)
		return
	}

	res, err := h.notes.UpdateNote(c.Request.Context(), body.ToProto(id))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.NoteFromProto(res.GetNote()))
}

func (h *NoteHandler) delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}

	if _, err := h.notes.DeleteNote(c.Request.Context(), &notev1.DeleteNoteRequest{Id: id}); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

var errInvalidBody = fmt.Errorf("%w: invalid JSON body", apperr.ErrInvalid)

func parseID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, apperr.Invalid("id", "must be a positive integer"))
		return 0, false
	}
	return id, true
}
