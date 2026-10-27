package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"core/outbox"

	"github.com/gin-gonic/gin"
)

// A sensitive topic's payload is emptied once delivered; set aside messages
// keep theirs, to be retried.
func TestHandleSensitiveScrubsDeliveredPayloads(t *testing.T) {
	db, u, ob := setup(t)
	publish(t, u, ob, context.Background(), "ok", "fails")

	relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{})
	relay.HandleSensitive("t", func(_ context.Context, m outbox.Message) error {
		if m.Key == "fails" {
			return outbox.Permanent(errors.New("recipient rejected"))
		}
		return nil
	})
	if _, err := relay.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	payload := func(key string) string {
		var p string
		db.Raw(`SELECT payload::text FROM outbox_messages WHERE key = ?`, key).Scan(&p)
		return p
	}
	if got := payload("ok"); got != "{}" {
		t.Fatalf("delivered payload kept: %s", got)
	}
	if got := payload("fails"); !strings.Contains(got, `"fails"`) {
		t.Fatalf("set aside payload lost: %s", got)
	}
}

// Permanent failures are set aside at the first attempt, and delivery goes on.
func TestPermanentFailureIsSetAsideAtOnce(t *testing.T) {
	db, u, ob := setup(t)
	publish(t, u, ob, context.Background(), "bad", "next")

	var delivered []string
	relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{MaxAttempts: 25})
	relay.Handle("t", func(_ context.Context, m outbox.Message) error {
		if m.Key == "bad" {
			return outbox.Permanent(errors.New("invalid address"))
		}
		delivered = append(delivered, m.Key)
		return nil
	})
	for i := 0; i < 2; i++ {
		if _, err := relay.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	var attempts int
	var failed bool
	db.Raw(`SELECT attempts, failed_at IS NOT NULL FROM outbox_messages WHERE key = 'bad'`).Row().Scan(&attempts, &failed)
	if !failed || attempts != 1 {
		t.Fatalf("permanent failure: attempts=%d set aside=%v", attempts, failed)
	}
	if len(delivered) != 1 || delivered[0] != "next" {
		t.Fatalf("delivery must go on after a set aside message: %v", delivered)
	}
	if err := outbox.Permanent(outbox.Permanent(errors.New("x"))); strings.Count(err.Error(), "permanent") != 1 {
		t.Fatalf("Permanent must not wrap twice: %v", err)
	}
}

func TestAdminRedactsSensitiveTopicsAndServesLocalOutbox(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, u, ob := setup(t)
	publish(t, u, ob, context.Background(), "k1")

	const key = "admin-test-key"
	local := outbox.NewAdminHandler(db, key, outbox.WithSensitiveTopics("t"))
	proxy, err := outbox.NewAdminProxy(key, map[string]string{"note": "http://127.0.0.1:1"}, http.DefaultTransport, nopLogger{},
		outbox.WithLocal("gateway", local))
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	proxy.RegisterRoutes(r)

	get := func(path string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	code, body := get("/admin/v1/outbox")
	var names struct{ Data []string }
	_ = json.Unmarshal([]byte(body), &names)
	if code != 200 || strings.Join(names.Data, ",") != "gateway,note" {
		t.Fatalf("services: %d %s", code, body)
	}

	code, body = get("/admin/v1/outbox/gateway/messages")
	if code != 200 || !strings.Contains(body, `"payload":"{\"redacted\":true}"`) || strings.Contains(body, `\"k\"`) {
		t.Fatalf("local messages must be served with redacted payloads: %d %s", code, body)
	}

	// the admin key still guards the local outbox
	req := httptest.NewRequest("GET", "/admin/v1/outbox/gateway/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("without the key: %d", w.Code)
	}

	if _, err := outbox.NewAdminProxy(key, map[string]string{"gateway": "http://gw"}, http.DefaultTransport, nopLogger{},
		outbox.WithLocal("gateway", local)); err == nil {
		t.Fatal("a name both local and remote must be refused")
	}
}
