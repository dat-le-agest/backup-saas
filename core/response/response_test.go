package response

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"core/apperr"

	"github.com/gin-gonic/gin"
)

func fail(err error) (int, map[string]any) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)
	Fail(c, err)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	e, _ := body["error"].(map[string]any)
	return w.Code, e
}

func TestFail(t *testing.T) {
	status, e := fail(fmt.Errorf("create: %w", apperr.Invalid("title", "is required")))
	details, _ := e["details"].([]any)
	if status != 400 || e["code"] != "invalid_input" || len(details) != 1 {
		t.Fatalf("validation: %d %v", status, e)
	}
	if d := details[0].(map[string]any); d["field"] != "title" || d["description"] != "is required" {
		t.Fatalf("detail: %v", d)
	}

	status, e = fail(apperr.ErrNotFound)
	if status != 404 || e["details"] != nil {
		t.Fatalf("not found: %d %v", status, e)
	}

	status, e = fail(errors.New("db: secret dsn"))
	if status != 500 || e["message"] != "Internal Server Error" || e["code"] != "internal" {
		t.Fatalf("internal: %d %v", status, e)
	}
}
