// Package model holds the admin service's data.
package model

import "time"

// Admin is an account of the admin dashboard.
type Admin struct {
	ID           string `gorm:"primaryKey;size:64"`
	Email        string `gorm:"size:254;not null;uniqueIndex"`
	PasswordHash string `gorm:"size:255;not null"`
	LastLoginAt  *time.Time
	CreatedAt    time.Time
}

// App is an app the dashboard manages, from configuration (ADMIN_APPS).
type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// AdminURL: the app's internal admin server (core/authn admin API), e.g. http://notes-gateway:9081
	AdminURL string `json:"admin_url"`
	// PublicURL: where the app's public API is, shown to admins.
	PublicURL string `json:"public_url"`
	// APIKey: the app's admin key (its ADMIN_API_KEY). Never sent to the browser.
	APIKey string `json:"api_key"`
}
