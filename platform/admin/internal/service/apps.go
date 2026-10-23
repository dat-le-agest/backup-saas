package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"admin/internal/model"
	"core/apperr"
)

var appIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Apps is the registry of managed apps (configuration) and talks to their
// admin APIs.
type Apps struct {
	list   []model.App
	byID   map[string]model.App
	client *http.Client
}

// ParseApps reads ADMIN_APPS: a JSON array of model.App.
func ParseApps(raw string) ([]model.App, error) {
	var apps []model.App
	if err := json.Unmarshal([]byte(raw), &apps); err != nil {
		return nil, fmt.Errorf("admin-apps: %w", err)
	}
	return apps, nil
}

func NewApps(apps []model.App, client *http.Client) (*Apps, error) {
	r := &Apps{byID: map[string]model.App{}, client: client}
	for _, a := range apps {
		if !appIDPattern.MatchString(a.ID) {
			return nil, fmt.Errorf("admin-apps: invalid app id %q", a.ID)
		}
		if _, dup := r.byID[a.ID]; dup {
			return nil, fmt.Errorf("admin-apps: app %q listed twice", a.ID)
		}
		u, err := url.Parse(a.AdminURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("admin-apps: app %q: invalid admin_url %q", a.ID, a.AdminURL)
		}
		if a.APIKey == "" {
			return nil, fmt.Errorf("admin-apps: app %q has no api_key", a.ID)
		}
		if a.Name == "" {
			a.Name = a.ID
		}
		r.list = append(r.list, a)
		r.byID[a.ID] = a
	}
	return r, nil
}

// Get returns a configured app, apperr.ErrNotFound otherwise.
func (r *Apps) Get(id string) (model.App, error) {
	a, ok := r.byID[id]
	if !ok {
		return model.App{}, fmt.Errorf("%w: no app %q", apperr.ErrNotFound, id)
	}
	return a, nil
}

// Status of an app, from its admin API.
type Status struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicURL string `json:"public_url"`
	Up        bool   `json:"up"`
	Error     string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Users     int64  `json:"users"`
	Tenants   int64  `json:"tenants"`
}

// Statuses asks every app at once (its /admin/v1/stats), each within a timeout.
func (r *Apps) Statuses(ctx context.Context) []Status {
	out := make([]Status, len(r.list))
	var wg sync.WaitGroup
	for i, a := range r.list {
		wg.Add(1)
		go func(i int, a model.App) {
			defer wg.Done()
			out[i] = r.status(ctx, a)
		}(i, a)
	}
	wg.Wait()
	return out
}

func (r *Apps) Status(ctx context.Context, id string) (Status, error) {
	a, err := r.Get(id)
	if err != nil {
		return Status{}, err
	}
	return r.status(ctx, a), nil
}

func (r *Apps) status(ctx context.Context, a model.App) Status {
	st := Status{ID: a.ID, Name: a.Name, PublicURL: a.PublicURL}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.AdminURL+"/admin/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	res, err := r.client.Do(req)
	st.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		st.Error = fmt.Sprintf("admin API answered %d", res.StatusCode)
		return st
	}
	var body struct {
		Data struct {
			Users   int64 `json:"users"`
			Tenants int64 `json:"tenants"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		st.Error = "invalid stats: " + err.Error()
		return st
	}
	st.Up, st.Users, st.Tenants = true, body.Data.Users, body.Data.Tenants
	return st
}
