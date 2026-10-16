package model

import "time"

// Note is tenant-owned: see the repository and migrations/00001_notes.sql
// (row-level security). The schema lives in migrations; gorm tags only describe it.
type Note struct {
	ID        uint   `gorm:"primaryKey;index:idx_notes_tenant_id_id,priority:2"`
	TenantID  string `gorm:"size:64;not null;index:idx_notes_tenant_id_id,priority:1"`
	CreatedBy string `gorm:"size:64;not null"`
	Title     string `gorm:"size:255;not null"`
	Content   string `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NoteView is a note as listed to a caller, with what the caller may do with it.
type NoteView struct {
	Note
	CanUpdate bool
	CanDelete bool
}

type NoteCreate struct {
	Title   string
	Content string
}

// NoteUpdate uses pointers so that omitted fields are left unchanged.
type NoteUpdate struct {
	Title   *string
	Content *string
}

type Paging struct {
	Page  int
	Limit int
	Total int64
}
