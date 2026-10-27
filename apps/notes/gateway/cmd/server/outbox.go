package main

import (
	"net/http"

	"core/logger"
	"core/mail"
	"core/outbox"
	"notes-gateway/config"

	"gorm.io/gorm"
)

// newOutboxAdmin: the app's outboxes in the admin API, the gateway's own
// (account mail, payloads hidden: they hold links with live tokens) and its
// services' (services maps a name to its base URL).
func newOutboxAdmin(adminKey string, db *gorm.DB, services map[string]string, transport http.RoundTripper, l logger.Logger) (*outbox.AdminProxy, error) {
	own := outbox.NewAdminHandler(db, adminKey, outbox.WithSensitiveTopics(mail.Topic))
	return outbox.NewAdminProxy(adminKey, services, transport, l, outbox.WithLocal(config.OutboxName, own))
}
