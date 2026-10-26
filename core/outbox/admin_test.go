package outbox_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"core/outbox"
	"core/testdb"

	"github.com/gin-gonic/gin"
)

func TestOutboxAdminHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.New(t)
	up := outbox.MigrationSQL[:len(outbox.MigrationSQL)-len("-- +goose Down\nDROP TABLE outbox_messages;\n")]
	if err := db.Exec(up).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// Insert 1 pending, 1 processed, 1 failed message
	db.Exec(`INSERT INTO outbox_messages (topic, key, payload, headers, created_at) VALUES ('test.created', 'k1', '{}', '{}', ?)`, now)
	db.Exec(`INSERT INTO outbox_messages (topic, key, payload, headers, created_at, processed_at) VALUES ('test.updated', 'k2', '{}', '{}', ?, ?)`, now, now)
	db.Exec(`INSERT INTO outbox_messages (topic, key, payload, headers, attempts, created_at, failed_at, last_error) VALUES ('test.deleted', 'k3', '{}', '{}', 25, ?, ?, 'permanent error')`, now, now)

	key := "admin-test-key"
	h := outbox.NewAdminHandler(db, key)
	r := gin.New()
	h.RegisterRoutes(r)

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. Stats
	w := do("GET", "/admin/v1/outbox/stats")
	if w.Code != http.StatusOK {
		t.Fatalf("stats status: %d %s", w.Code, w.Body.String())
	}
	var statsRes struct {
		Data outbox.StatsView `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &statsRes); err != nil {
		t.Fatal(err)
	}
	if statsRes.Data.Total != 3 || statsRes.Data.Pending != 1 || statsRes.Data.Processed != 1 || statsRes.Data.Failed != 1 {
		t.Fatalf("unexpected stats: %+v", statsRes.Data)
	}

	// 2. List all
	w = do("GET", "/admin/v1/outbox/messages")
	if w.Code != http.StatusOK {
		t.Fatalf("list status: %d", w.Code)
	}
	var listRes struct {
		Data   []outbox.MessageView `json:"data"`
		Paging struct {
			Total int64 `json:"total"`
		} `json:"paging"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listRes); err != nil {
		t.Fatal(err)
	}
	if listRes.Paging.Total != 3 || len(listRes.Data) != 3 {
		t.Fatalf("unexpected list count: %+v", listRes)
	}

	// 3. Retry single failed message
	var failedID int64
	for _, m := range listRes.Data {
		if m.Status == "failed" {
			failedID = m.ID
			break
		}
	}
	if failedID == 0 {
		t.Fatal("no failed message found")
	}

	w = do("POST", "/admin/v1/outbox/messages/"+string(rune('0'+failedID))+"/retry")
	if w.Code != http.StatusNoContent {
		t.Fatalf("retry status: %d", w.Code)
	}

	// Verify failed message is now pending (failed_at is NULL, attempts is 0)
	w = do("GET", "/admin/v1/outbox/stats")
	_ = json.Unmarshal(w.Body.Bytes(), &statsRes)
	if statsRes.Data.Failed != 0 || statsRes.Data.Pending != 2 {
		t.Fatalf("stats after retry: %+v", statsRes.Data)
	}

	// 4. Retry all
	db.Exec(`UPDATE outbox_messages SET failed_at = now(), attempts = 25 WHERE id = ?`, failedID)
	w = do("POST", "/admin/v1/outbox/retry-all")
	if w.Code != http.StatusOK {
		t.Fatalf("retry-all status: %d", w.Code)
	}
	w = do("GET", "/admin/v1/outbox/stats")
	_ = json.Unmarshal(w.Body.Bytes(), &statsRes)
	if statsRes.Data.Failed != 0 || statsRes.Data.Pending != 2 {
		t.Fatalf("stats after retry-all: %+v", statsRes.Data)
	}
}
