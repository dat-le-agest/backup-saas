package dialets

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// /tmp/gorm.db
func SQLiteDB(dsn string) (db *gorm.DB, err error) {
	return gorm.Open(sqlite.Open(dsn), &gorm.Config{})
}
