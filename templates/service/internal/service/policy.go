package service

// ResourceNote is the resource type of note checks: notes are "note:<id>" in
// the model (authz/authz.yml), created and listed through the tenant
// (tenant:<id>#can_create_note, can_read_note).
const ResourceNote = "note"
