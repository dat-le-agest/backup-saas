package rebac

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"core/apperr"
	"core/authz/rebac/engine"
	"core/authz/rebac/store"
	"core/httpx"
	"core/response"

	"github.com/gin-gonic/gin"
)

// AdminHandler is the engine's part of an app's admin API, for the platform's
// admin dashboard: the model versions, the stored tuples, checks and the
// changelog. Serve it on an internal port only: every request must carry
// "Authorization: Bearer <admin key>". The admin service names the signed-in
// admin in X-Admin-Actor; tuple changes are recorded as theirs.
type AdminHandler struct {
	e   *Engine
	key string
}

// NewAdminHandler: key is the app's admin key; empty refuses every request.
func NewAdminHandler(e *Engine, key string) *AdminHandler {
	return &AdminHandler{e: e, key: key}
}

// ActorHeader names the admin making a change (set by the admin service).
const ActorHeader = "X-Admin-Actor"

// RegisterRoutes mounts /admin/v1/authz/* on r.
func (h *AdminHandler) RegisterRoutes(r gin.IRouter) {
	g := r.Group("/admin/v1/authz", httpx.AdminKey(h.key), h.actor)
	g.GET("/models", h.listModels)
	g.GET("/models/:id", h.getModel)
	g.POST("/models/:id/activate", h.activateModel)

	g.GET("/tuples", h.listTuples)
	g.POST("/tuples", h.writeTuples)
	g.POST("/check", h.check)
	g.POST("/list-objects", h.listObjects)
	g.GET("/changes", h.changes)
}

func (h *AdminHandler) actor(c *gin.Context) {
	if a := strings.TrimSpace(c.GetHeader(ActorHeader)); a != "" {
		c.Request = c.Request.WithContext(WithActor(c.Request.Context(), "admin:"+a))
	}
	c.Next()
}

// ---- models ----

type AdminModel struct {
	ID            string          `json:"id"`
	SchemaVersion string          `json:"schema_version"`
	IsActive      bool            `json:"is_active"`
	CreatedBy     string          `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
	Source        string          `json:"source,omitempty"` // the YAML files it was composed from
	Types         json.RawMessage `json:"types,omitempty"`  // compiled: {"<type>": {"relations": {...}}}
}

func adminModel(m *store.StoredModel) AdminModel {
	return AdminModel{ID: m.ID, SchemaVersion: m.SchemaVersion, IsActive: m.IsActive, CreatedBy: m.CreatedBy, CreatedAt: m.CreatedAt}
}

func (h *AdminHandler) listModels(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	ms, err := h.e.ListModels(c.Request.Context(), limit)
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]AdminModel, 0, len(ms))
	for i := range ms {
		out = append(out, adminModel(&ms[i]))
	}
	response.OK(c, http.StatusOK, out)
}

// getModel: id "active" is the active model.
func (h *AdminHandler) getModel(c *gin.Context) {
	id := c.Param("id")
	if id == "active" {
		id = ""
	}
	m, err := h.e.GetModel(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	var compiled struct {
		Types json.RawMessage `json:"types"`
	}
	if err := json.Unmarshal(m.Model, &compiled); err != nil {
		response.Fail(c, err)
		return
	}
	out := adminModel(m)
	out.Source, out.Types = m.DSL, compiled.Types
	response.OK(c, http.StatusOK, out)
}

func (h *AdminHandler) activateModel(c *gin.Context) {
	if err := h.e.ActivateModel(c.Request.Context(), c.Param("id")); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// ---- tuples ----

type AdminTuple struct {
	Object   string `json:"object"`
	Relation string `json:"relation"`
	Subject  string `json:"subject"`
	TenantID string `json:"tenant_id"`
}

type Paging struct {
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
	Total int64 `json:"total"`
}

// listTuples filters: tenant_id, object ("note" or "note:42"), relation,
// subject ("user", "user:u1" or "team:eng#member").
func (h *AdminHandler) listTuples(c *gin.Context) {
	f := store.TupleFilter{TenantID: c.Query("tenant_id"), Relation: c.Query("relation")}
	f.ObjectType, f.ObjectID, _ = strings.Cut(c.Query("object"), ":")
	if sub := c.Query("subject"); sub != "" {
		ref, rel, hasRel := strings.Cut(sub, "#")
		f.SubjectType, f.SubjectID, _ = strings.Cut(ref, ":")
		f.SubjectRelation, f.HasSubjectRelation = rel, hasRel
	}
	page, _ := strconv.Atoi(c.Query("page"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	ts, total, err := h.e.ReadTuples(c.Request.Context(), f, (page-1)*limit, limit)
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]AdminTuple, 0, len(ts))
	for _, t := range ts {
		out = append(out, adminTuple(t))
	}
	response.List(c, out, Paging{Page: page, Limit: limit, Total: total})
}

func adminTuple(t engine.Tuple) AdminTuple {
	return AdminTuple{Object: t.Object.String(), Relation: t.Relation, Subject: t.Subject.String(), TenantID: t.TenantID}
}

func inputsOf(ts []AdminTuple) []TupleInput {
	out := make([]TupleInput, len(ts))
	for i, t := range ts {
		out[i] = TupleInput{Object: t.Object, Relation: t.Relation, Subject: t.Subject, TenantID: t.TenantID}
	}
	return out
}

type WriteTuplesRequest struct {
	Writes  []AdminTuple `json:"writes"`
	Deletes []AdminTuple `json:"deletes"`
}

func (h *AdminHandler) writeTuples(c *gin.Context) {
	var body WriteTuplesRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	for i, t := range body.Writes {
		if t.TenantID == "" {
			response.Fail(c, apperr.Invalid("writes["+strconv.Itoa(i)+"].tenant_id", "is required"))
			return
		}
	}
	if err := h.e.WriteTuples(c.Request.Context(), inputsOf(body.Writes), inputsOf(body.Deletes), nil); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// ---- checks ----

// CheckRequest: Contextual tuples count for this check only, e.g. the role a
// user would have in a tenant (tenant:t1#member@user:u1): roles aren't stored.
type CheckRequest struct {
	Object     string       `json:"object"`
	Relation   string       `json:"relation"`
	Subject    string       `json:"subject"`
	Contextual []AdminTuple `json:"contextual"`
}

func (h *AdminHandler) check(c *gin.Context) {
	var body CheckRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	ok, err := h.e.Check(c.Request.Context(), body.Object, body.Relation, body.Subject, inputsOf(body.Contextual), Higher)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"allowed": ok})
}

type ListObjectsRequest struct {
	Type       string       `json:"type"`
	Relation   string       `json:"relation"`
	Subject    string       `json:"subject"`
	Contextual []AdminTuple `json:"contextual"`
	Limit      int          `json:"limit"`
}

func (h *AdminHandler) listObjects(c *gin.Context) {
	var body ListObjectsRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	objs, err := h.e.ListObjects(c.Request.Context(), body.Type, body.Relation, body.Subject, inputsOf(body.Contextual), body.Limit, Higher)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"objects": objs})
}

// ---- changelog ----

type AdminChange struct {
	Seq       int64  `json:"seq"`
	Operation string `json:"operation"`
	AdminTuple
	Actor     string    `json:"actor"`
	ChangedAt time.Time `json:"changed_at"`
}

// changes: newest first, before before_seq (0 = the latest), filtered by
// object_type and tenant_id. Paging.next is the before_seq of the next page
// (0: no more).
func (h *AdminHandler) changes(c *gin.Context) {
	before, _ := strconv.ParseInt(c.Query("before_seq"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	cs, err := h.e.RecentChanges(c.Request.Context(), c.Query("object_type"), c.Query("tenant_id"), before, limit)
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]AdminChange, 0, len(cs))
	for _, ch := range cs {
		out = append(out, AdminChange{Seq: ch.Seq, Operation: ch.Operation, AdminTuple: adminTuple(ch.Tuple), Actor: ch.Actor, ChangedAt: ch.ChangedAt})
	}
	var next int64
	if len(cs) == limit {
		next = cs[len(cs)-1].Seq
	}
	response.List(c, out, gin.H{"next": next})
}
