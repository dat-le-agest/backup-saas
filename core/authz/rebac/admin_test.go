package rebac

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	zaplogger "core/logger/zap"

	"github.com/gin-gonic/gin"
)

func newAdminServer(t *testing.T) *httptest.Server {
	t.Helper()
	e := newService(t)
	if _, err := e.WriteModel(context.Background(), model, true); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewAdminHandler(e, "k3y").RegisterRoutes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

type adminResp struct {
	Data   json.RawMessage `json:"data"`
	Paging json.RawMessage `json:"paging"`
}

func call(t *testing.T, base, key, method, path, body string, want int) adminResp {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(ActorHeader, "ops@example.com")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out adminResp
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d", method, path, res.StatusCode, want)
	}
	return out
}

func TestAdminHandler(t *testing.T) {
	srv := newAdminServer(t)
	u := srv.URL

	call(t, u, "", "GET", "/admin/v1/authz/models", "", http.StatusUnauthorized)
	call(t, u, "wrong", "GET", "/admin/v1/authz/models", "", http.StatusUnauthorized)

	var models []AdminModel
	_ = json.Unmarshal(call(t, u, "k3y", "GET", "/admin/v1/authz/models", "", http.StatusOK).Data, &models)
	if len(models) != 1 || !models[0].IsActive {
		t.Fatalf("models = %+v", models)
	}
	var active AdminModel
	_ = json.Unmarshal(call(t, u, "k3y", "GET", "/admin/v1/authz/models/active", "", http.StatusOK).Data, &active)
	if active.ID != models[0].ID || !strings.Contains(active.Source, "team#member") || !strings.Contains(string(active.Types), `"note"`) {
		t.Fatalf("active model = %+v", active)
	}

	call(t, u, "k3y", "POST", "/admin/v1/authz/tuples", `{"writes":[
		{"object":"note:1","relation":"tenant","subject":"tenant:tnt_a","tenant_id":"tnt_a"},
		{"object":"note:1","relation":"viewer","subject":"user:bob","tenant_id":"tnt_a"},
		{"object":"team:eng","relation":"member","subject":"user:amy","tenant_id":"tnt_b"}]}`, http.StatusNoContent)
	// tenant_id is required; tuples are validated against the model
	call(t, u, "k3y", "POST", "/admin/v1/authz/tuples", `{"writes":[{"object":"note:1","relation":"viewer","subject":"user:x"}]}`, http.StatusBadRequest)
	call(t, u, "k3y", "POST", "/admin/v1/authz/tuples", `{"writes":[{"object":"note:1","relation":"nope","subject":"user:x","tenant_id":"t"}]}`, http.StatusBadRequest)

	for _, tc := range []struct {
		query string
		total int
	}{
		{"", 3},
		{"?tenant_id=tnt_a", 2},
		{"?object=note", 2},
		{"?object=note:1&relation=viewer", 1},
		{"?subject=user", 2},
		{"?subject=user:amy", 1},
		{"?subject=tenant:tnt_a", 1},
	} {
		var paging Paging
		_ = json.Unmarshal(call(t, u, "k3y", "GET", "/admin/v1/authz/tuples"+tc.query, "", http.StatusOK).Paging, &paging)
		if paging.Total != int64(tc.total) {
			t.Errorf("tuples%s: total %d, want %d", tc.query, paging.Total, tc.total)
		}
	}

	var check struct{ Allowed bool }
	_ = json.Unmarshal(call(t, u, "k3y", "POST", "/admin/v1/authz/check", `{"object":"note:1","relation":"viewer","subject":"user:bob"}`, http.StatusOK).Data, &check)
	if !check.Allowed {
		t.Error("bob should view note:1")
	}
	// roles aren't stored: given as contextual tuples
	_ = json.Unmarshal(call(t, u, "k3y", "POST", "/admin/v1/authz/check", `{"object":"note:1","relation":"editor","subject":"user:cat",
		"contextual":[{"object":"tenant:tnt_a","relation":"admin","subject":"user:cat","tenant_id":"tnt_a"}]}`, http.StatusOK).Data, &check)
	if !check.Allowed {
		t.Error("a tenant admin should edit note:1")
	}
	var list struct{ Objects []string }
	_ = json.Unmarshal(call(t, u, "k3y", "POST", "/admin/v1/authz/list-objects", `{"type":"note","relation":"viewer","subject":"user:bob"}`, http.StatusOK).Data, &list)
	if len(list.Objects) != 1 || list.Objects[0] != "note:1" {
		t.Errorf("list-objects = %v", list.Objects)
	}

	call(t, u, "k3y", "POST", "/admin/v1/authz/tuples", `{"deletes":[{"object":"note:1","relation":"viewer","subject":"user:bob"}]}`, http.StatusNoContent)
	var changes []AdminChange
	_ = json.Unmarshal(call(t, u, "k3y", "GET", "/admin/v1/authz/changes?tenant_id=tnt_a", "", http.StatusOK).Data, &changes)
	if len(changes) != 3 || changes[0].Operation == changes[1].Operation || changes[0].Actor != "admin:ops@example.com" {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].Seq < changes[1].Seq {
		t.Error("changes should be newest first")
	}
}

func TestAdminProxy(t *testing.T) {
	srv := newAdminServer(t)
	p, err := NewAdminProxy("k3y", map[string]string{"note": srv.URL}, http.DefaultTransport, zaplogger.NewZapLogger().GetLogger("test"))
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	p.RegisterRoutes(r)
	gw := httptest.NewServer(r)
	t.Cleanup(gw.Close)

	call(t, gw.URL, "", "GET", "/admin/v1/authz", "", http.StatusUnauthorized)
	var names []string
	_ = json.Unmarshal(call(t, gw.URL, "k3y", "GET", "/admin/v1/authz", "", http.StatusOK).Data, &names)
	if len(names) != 1 || names[0] != "note" {
		t.Fatalf("services = %v", names)
	}
	var models []AdminModel
	_ = json.Unmarshal(call(t, gw.URL, "k3y", "GET", "/admin/v1/authz/note/models", "", http.StatusOK).Data, &models)
	if len(models) != 1 {
		t.Fatalf("models via proxy = %+v", models)
	}
	call(t, gw.URL, "k3y", "GET", "/admin/v1/authz/other/models", "", http.StatusNotFound)
}
