package service

import "sdk/authz"

const ResourceNote = "note"

// NotePolicy: who may do what with notes inside a tenant.
//
//	owner, admin  everything
//	member        create, read all, update/delete own
//	viewer        read all
var NotePolicy = authz.Policy{
	authz.RoleOwner: {
		authz.Perm(ResourceNote, authz.Create): authz.Any,
		authz.Perm(ResourceNote, authz.Read):   authz.Any,
		authz.Perm(ResourceNote, authz.Update): authz.Any,
		authz.Perm(ResourceNote, authz.Delete): authz.Any,
	},
	authz.RoleAdmin: {
		authz.Perm(ResourceNote, authz.Create): authz.Any,
		authz.Perm(ResourceNote, authz.Read):   authz.Any,
		authz.Perm(ResourceNote, authz.Update): authz.Any,
		authz.Perm(ResourceNote, authz.Delete): authz.Any,
	},
	authz.RoleMember: {
		authz.Perm(ResourceNote, authz.Create): authz.Own,
		authz.Perm(ResourceNote, authz.Read):   authz.Any,
		authz.Perm(ResourceNote, authz.Update): authz.Own,
		authz.Perm(ResourceNote, authz.Delete): authz.Own,
	},
	authz.RoleViewer: {
		authz.Perm(ResourceNote, authz.Read): authz.Any,
	},
}
