package dialets

import (
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// user:password@tcp(localhost:3306)/dbname?charset=utf8mb4&parseTime=True&loc=Local
func MySQLDB(dsn string) (db *gorm.DB, error error) {
	return gorm.Open(mysql.Open(dsn), &gorm.Config{})
}
