package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"admin/internal/handler"
	"admin/internal/model"
	"admin/internal/service"
	"admin/internal/store"
	"admin/migrations"
	"core/authn/password"
	zaplogger "core/logger/zap"
	"core/testdb"
	"core/token"

	"github.com/gin-gonic/gin"
)

// fakeApp is an app's admin API: it records what it receives.
type fakeApp struct {
	mu       sync.Mutex
	key      string
	requests []string
	auth     []string
	cookies  []string
	actors   []string
}

func (f *fakeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.cookies = append(f.cookies, r.Header.Get("Cookie"))
	f.actors = append(f.actors, r.Header.Get("X-Admin-Actor"))
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.key {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/admin/v1/stats":
		_, _ = w.Write([]byte(`{"data":{"app":"notes","users":3,"tenants":2}}`))
	case r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`{"data":[{"id":"usr_1","email":"a@example.com"}],"paging":{"page":1,"limit":20,"total":1}}`))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type env struct {
	r    *gin.Engine
	app  *fakeApp
	auth *service.Admins
}

func newEnv(t *testing.T) *env {
	t.Helper()
	app := &fakeApp{key: "notes-key"}
	appSrv := httptest.NewServer(app)
	t.Cleanup(appSrv.Close)

	key, _ := token.GenerateKey()
	admins := service.NewAdmins(store.New(testdb.New(t, migrations.FS)),
		password.NewHasher(password.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 0),
		token.NewSigner(key, "saas-iam", time.Hour), "admin")
	if created, err := admins.Bootstrap(context.Background(), "root@example.com", "a long admin password"); err != nil || !created {
		t.Fatalf("bootstrap: %v %v", created, err)
	}
	// once only
	if created, _ := admins.Bootstrap(context.Background(), "other@example.com", "a long admin password"); created {
		t.Fatal("bootstrap created a second admin")
	}

	apps, err := service.NewApps([]model.App{
		{ID: "notes", Name: "Notes", AdminURL: appSrv.URL, PublicURL: "http://localhost:8081", APIKey: "notes-key"},
		{ID: "crm", AdminURL: "http://127.0.0.1:1", APIKey: "crm-key"}, // nobody listens
	}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	handler.New(admins, apps, zaplogger.NewZapLogger().GetLogger("test"), handler.Options{}).Register(r, 600, 100)
	return &env{r: r, app: app, auth: admins}
}

type result struct {
	status int
	body   map[string]any
	header http.Header
}

func (e *env) do(method, path, body, cookie string) result {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" || method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: handler.SessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	var parsed map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	return result{status: w.Code, body: parsed, header: w.Header()}
}

func (e *env) signIn(t *testing.T) string {
	t.Helper()
	res := e.do("POST", "/api/auth/login", `{"email":"ROOT@example.com","password":"a long admin password"}`, "")
	if res.status != 200 {
		t.Fatalf("login: %d %v", res.status, res.body)
	}
	for _, c := range (&http.Response{Header: res.header}).Cookies() {
		if c.Name == handler.SessionCookie {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("session cookie flags: %+v", c)
			}
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func TestSignIn(t *testing.T) {
	e := newEnv(t)
	if res := e.do("POST", "/api/auth/login", `{"email":"root@example.com","password":"wrong password!!"}`, ""); res.status != 401 {
		t.Fatalf("wrong password: %d", res.status)
	}
	if res := e.do("POST", "/api/auth/login", `{"email":"ghost@example.com","password":"a long admin password"}`, ""); res.status != 401 {
		t.Fatalf("unknown admin: %d", res.status)
	}
	if res := e.do("GET", "/api/me", "", ""); res.status != 401 {
		t.Fatalf("me without session: %d", res.status)
	}
	if res := e.do("GET", "/api/me", "", "forged"); res.status != 401 {
		t.Fatalf("me with a forged session: %d", res.status)
	}

	session := e.signIn(t)
	if res := e.do("GET", "/api/me", "", session); res.status != 200 || res.body["data"].(map[string]any)["email"] != "root@example.com" {
		t.Fatalf("me: %d %v", res.status, res.body)
	}

	// a form POST (no preflight cross-site) is refused: no CSRF with the cookie
	req := httptest.NewRequest("POST", "/api/admins", strings.NewReader("email=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: handler.SessionCookie, Value: session})
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("form post: %d", w.Code)
	}

	if res := e.do("POST", "/api/admins", `{"email":"second@example.com","password":"another long password"}`, session); res.status != 201 {
		t.Fatalf("create admin: %d %v", res.status, res.body)
	}
	if res := e.do("GET", "/api/admins", "", session); res.status != 200 || len(res.body["data"].([]any)) != 2 {
		t.Fatalf("list admins: %d %v", res.status, res.body)
	}
	if res := e.do("POST", "/api/auth/logout", `{}`, session); res.status != 204 {
		t.Fatalf("logout: %d", res.status)
	}
}

func TestAppsAndProxy(t *testing.T) {
	e := newEnv(t)
	session := e.signIn(t)

	res := e.do("GET", "/api/apps", "", session)
	apps, _ := res.body["data"].([]any)
	if res.status != 200 || len(apps) != 2 {
		t.Fatalf("apps: %d %v", res.status, res.body)
	}
	notes, crm := apps[0].(map[string]any), apps[1].(map[string]any)
	if notes["up"] != true || notes["users"] != float64(3) || notes["tenants"] != float64(2) || notes["name"] != "Notes" {
		t.Fatalf("notes status: %v", notes)
	}
	if crm["up"] != false || crm["error"] == "" || crm["name"] != "crm" {
		t.Fatalf("unreachable app status: %v", crm)
	}
	if _, ok := notes["api_key"]; ok {
		t.Fatal("api key sent to the browser")
	}

	// forwarded to the app's admin API with its key, never the admin's cookie
	res = e.do("GET", "/api/apps/notes/admin/users?q=a&page=2", "", session)
	if res.status != 200 || res.body["paging"] == nil {
		t.Fatalf("proxied users: %d %v", res.status, res.body)
	}
	if res := e.do("PUT", "/api/apps/notes/admin/users/usr_1/disabled", `{"disabled":true}`, session); res.status != 204 {
		t.Fatalf("proxied change: %d %v", res.status, res.body)
	}
	e.app.mu.Lock()
	reqs, auth, cookies := e.app.requests, e.app.auth, e.app.cookies
	e.app.mu.Unlock()
	last := len(reqs) - 1
	if reqs[last-1] != "GET /admin/v1/users?q=a&page=2" || reqs[last] != "PUT /admin/v1/users/usr_1/disabled" {
		t.Fatalf("forwarded: %v", reqs)
	}
	if auth[last] != "Bearer notes-key" || cookies[last] != "" {
		t.Fatalf("forwarded credentials: auth=%q cookie=%q", auth[last], cookies[last])
	}

	// the app's authorization engines: tuple changes are recorded as the admin's
	if res := e.do("POST", "/api/apps/notes/admin/authz/note/tuples", `{"writes":[]}`, session); res.status != 204 {
		t.Fatalf("proxied authz change: %d %v", res.status, res.body)
	}
	e.app.mu.Lock()
	reqs, actors := e.app.requests, e.app.actors
	e.app.mu.Unlock()
	if reqs[len(reqs)-1] != "POST /admin/v1/authz/note/tuples" || actors[len(actors)-1] != "root@example.com" {
		t.Fatalf("forwarded authz: %v actor=%q", reqs[len(reqs)-1], actors[len(actors)-1])
	}

	for name, path := range map[string]string{
		"not an admin path":  "/api/apps/notes/admin/v1/../../secret",
		"unknown authz path": "/api/apps/notes/admin/authz/note/secret",
		"unknown app":        "/api/apps/nope/admin/users",
		"other resource":     "/api/apps/notes/admin/config",
	} {
		if res := e.do("GET", path, "", session); res.status != 404 {
			t.Errorf("%s: %d", name, res.status)
		}
	}
	if res := e.do("GET", "/api/apps/notes/admin/users", "", ""); res.status != 401 {
		t.Fatalf("proxy without session: %d", res.status)
	}
	if res := e.do("GET", "/api/apps/crm/admin/users", "", session); res.status != 503 {
		t.Fatalf("unreachable app: %d %v", res.status, res.body)
	}
}
