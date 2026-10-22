package gatewaytest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

// Result is a gateway response with its JSON body decoded.
type Result struct {
	Status int
	Header http.Header
	Body   map[string]any
}

// Do sends a JSON request to h. bearer, if set, becomes the Authorization
// header; headers are extra name, value pairs.
func Do(h http.Handler, method, path, body, bearer string, headers ...string) Result {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var parsed map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	return Result{Status: w.Code, Header: w.Header(), Body: parsed}
}

// Data is the "data" object of a success response.
func (r Result) Data() map[string]any {
	m, _ := r.Body["data"].(map[string]any)
	return m
}

// ErrCode is error.code of an error response.
func (r Result) ErrCode() string { return r.errField("code") }

// ErrMsg is error.message of an error response.
func (r Result) ErrMsg() string { return r.errField("message") }

func (r Result) errField(name string) string {
	e, _ := r.Body["error"].(map[string]any)
	s, _ := e[name].(string)
	return s
}
