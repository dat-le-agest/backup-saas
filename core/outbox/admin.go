package outbox

import (
	"net/http"
	"strconv"
	"time"

	"core/apperr"
	"core/httpx"
	"core/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AdminHandler exposes the outbox of a service for the platform's admin dashboard.
type AdminHandler struct {
	db        *gorm.DB
	key       string
	sensitive map[string]bool
}

type AdminOption func(*AdminHandler)

// WithSensitiveTopics hides the payload of these topics (Relay.HandleSensitive):
// pending and set aside messages still hold it.
func WithSensitiveTopics(topics ...string) AdminOption {
	return func(h *AdminHandler) {
		for _, t := range topics {
			h.sensitive[t] = true
		}
	}
}

func NewAdminHandler(db *gorm.DB, key string, opts ...AdminOption) *AdminHandler {
	h := &AdminHandler{db: db, key: key, sensitive: map[string]bool{}}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RedactedPayload replaces the payload of sensitive topics in the admin API.
const RedactedPayload = `{"redacted":true}`

// RegisterRoutes mounts /admin/v1/outbox/* on r.
func (h *AdminHandler) RegisterRoutes(r gin.IRouter) {
	g := r.Group("/admin/v1/outbox", httpx.AdminKey(h.key))
	g.GET("/stats", h.stats)
	g.GET("/messages", h.list)
	g.POST("/messages/:id/retry", h.retry)
	g.POST("/retry-all", h.retryAll)
}

type MessageView struct {
	ID            int64      `json:"id" gorm:"column:id"`
	Topic         string     `json:"topic" gorm:"column:topic"`
	Key           string     `json:"key" gorm:"column:key"`
	Payload       string     `json:"payload" gorm:"column:payload"`
	Headers       string     `json:"headers" gorm:"column:headers"`
	Attempts      int        `json:"attempts" gorm:"column:attempts"`
	NextAttemptAt time.Time  `json:"next_attempt_at" gorm:"column:next_attempt_at"`
	LastError     string     `json:"last_error" gorm:"column:last_error"`
	CreatedAt     time.Time  `json:"created_at" gorm:"column:created_at"`
	ProcessedAt   *time.Time `json:"processed_at" gorm:"column:processed_at"`
	FailedAt      *time.Time `json:"failed_at" gorm:"column:failed_at"`
	Status        string     `json:"status" gorm:"-"`
}

type StatsView struct {
	Total     int64 `json:"total"`
	Pending   int64 `json:"pending"`
	Processed int64 `json:"processed"`
	Failed    int64 `json:"failed"`
}

func (h *AdminHandler) stats(c *gin.Context) {
	var stats StatsView
	ctx := c.Request.Context()

	h.db.WithContext(ctx).Table("outbox_messages").Count(&stats.Total)
	h.db.WithContext(ctx).Table("outbox_messages").Where("processed_at IS NULL AND failed_at IS NULL").Count(&stats.Pending)
	h.db.WithContext(ctx).Table("outbox_messages").Where("processed_at IS NOT NULL").Count(&stats.Processed)
	h.db.WithContext(ctx).Table("outbox_messages").Where("failed_at IS NOT NULL").Count(&stats.Failed)

	response.OK(c, http.StatusOK, stats)
}

func (h *AdminHandler) list(c *gin.Context) {
	status := c.Query("status")
	topic := c.Query("topic")
	page, _ := strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	q := h.db.WithContext(c.Request.Context()).Table("outbox_messages")
	if topic != "" {
		q = q.Where("topic = ?", topic)
	}

	switch status {
	case "pending":
		q = q.Where("processed_at IS NULL AND failed_at IS NULL")
	case "failed":
		q = q.Where("failed_at IS NOT NULL")
	case "processed":
		q = q.Where("processed_at IS NOT NULL")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Fail(c, err)
		return
	}

	var msgs []MessageView
	offset := (page - 1) * limit
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&msgs).Error; err != nil {
		response.Fail(c, err)
		return
	}

	for i := range msgs {
		if h.sensitive[msgs[i].Topic] {
			msgs[i].Payload = RedactedPayload
		}
		if msgs[i].ProcessedAt != nil {
			msgs[i].Status = "processed"
		} else if msgs[i].FailedAt != nil {
			msgs[i].Status = "failed"
		} else {
			msgs[i].Status = "pending"
		}
	}

	response.List(c, msgs, gin.H{
		"page":  page,
		"limit": limit,
		"total": total,
	})
}

func (h *AdminHandler) retry(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, apperr.ErrInvalid)
		return
	}

	res := h.db.WithContext(c.Request.Context()).Exec(
		`UPDATE outbox_messages SET failed_at = NULL, attempts = 0, next_attempt_at = now() WHERE id = ?`,
		id,
	)
	if res.Error != nil {
		response.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		response.Fail(c, apperr.ErrNotFound)
		return
	}

	response.NoContent(c)
}

func (h *AdminHandler) retryAll(c *gin.Context) {
	res := h.db.WithContext(c.Request.Context()).Exec(
		`UPDATE outbox_messages SET failed_at = NULL, attempts = 0, next_attempt_at = now() WHERE failed_at IS NOT NULL`,
	)
	if res.Error != nil {
		response.Fail(c, res.Error)
		return
	}

	response.OK(c, http.StatusOK, gin.H{"retried": res.RowsAffected})
}
