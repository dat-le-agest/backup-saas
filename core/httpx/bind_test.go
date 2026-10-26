package httpx

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type bindItem struct {
	Name string `json:"name" validate:"required"`
}

type bindBody struct {
	Title string     `json:"title" validate:"required,max=5"`
	Note  *string    `json:"note" validate:"omitempty,min=1"`
	Role  string     `json:"role" validate:"omitempty,oneof=owner member"`
	Count int        `json:"count" validate:"omitempty,max=10"`
	Items []bindItem `json:"items" validate:"dive"`
}

type bindQuery struct {
	Limit int32 `form:"limit" validate:"omitempty,min=1,max=100"`
}

type detail struct{ Field, Description string }

func bindRequest(t *testing.T, target, body string) (int, string, []detail) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/", func(c *gin.Context) {
		var b bindBody
		if BindJSON(c, &b) {
			c.Status(204)
		}
	})
	r.GET("/", func(c *gin.Context) {
		var q bindQuery
		if BindQuery(c, &q) {
			c.Status(204)
		}
	})
	method := "POST"
	if body == "" {
		method = "GET"
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, target, strings.NewReader(body)))

	var res struct {
		Error struct {
			Message string   `json:"message"`
			Details []detail `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	return w.Code, res.Error.Message, res.Error.Details
}

func TestBindJSONValidatesTags(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		details    []detail
	}{
		{"valid", `{"title":"hi","note":"x","role":"owner","items":[{"name":"a"}]}`, 204, nil},
		{"required", `{}`, 400, []detail{{"title", "is required"}}},
		{"max runes", `{"title":"ééééé"}`, 204, nil},
		{"too long", `{"title":"abcdef"}`, 400, []detail{{"title", "must be at most 5 characters"}}},
		{"all violations", `{"title":"","note":"","role":"x","count":11}`, 400, []detail{
			{"title", "is required"},
			{"note", "must be at least 1 character"},
			{"role", "must be one of owner, member"},
			{"count", "must be at most 10"},
		}},
		{"nested", `{"title":"hi","items":[{"name":"a"},{}]}`, 400, []detail{{"items[1].name", "is required"}}},
		{"wrong type", `{"title":123}`, 400, []detail{{"title", "must be a string"}}},
	}
	for _, tc := range cases {
		status, _, details := bindRequest(t, "/", tc.body)
		if status != tc.status || !reflect.DeepEqual(details, tc.details) {
			t.Errorf("%s: got %d %+v", tc.name, status, details)
		}
	}
}

func TestBindJSONMalformedBody(t *testing.T) {
	status, msg, details := bindRequest(t, "/", `{`)
	if status != 400 || msg != ErrInvalidBody.Error() || details != nil {
		t.Fatalf("got %d %q %+v", status, msg, details)
	}
}

func TestBindQuery(t *testing.T) {
	if status, _, _ := bindRequest(t, "/?limit=20", ""); status != 204 {
		t.Fatalf("valid: %d", status)
	}
	status, _, details := bindRequest(t, "/?limit=500", "")
	if status != 400 || !reflect.DeepEqual(details, []detail{{"limit", "must be at most 100"}}) {
		t.Fatalf("too big: %d %+v", status, details)
	}
	if status, msg, _ := bindRequest(t, "/?limit=abc", ""); status != 400 || msg != ErrInvalidQuery.Error() {
		t.Fatalf("not a number: %d %q", status, msg)
	}
}
