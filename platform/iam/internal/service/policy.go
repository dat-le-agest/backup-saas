package service

import "core/authz"

const ResourceMember = "member"

// MemberPolicy: any role may list members; owners and admins manage them.
// Rules involving owners (admins can't touch or grant owner, the last owner
// stays) are enforced in MemberService on top of this policy.
var MemberPolicy = authz.Policy{
	authz.RoleOwner: {
		authz.Perm(ResourceMember, authz.Read):   authz.Any,
		authz.Perm(ResourceMember, authz.Create): authz.Any,
		authz.Perm(ResourceMember, authz.Update): authz.Any,
		authz.Perm(ResourceMember, authz.Delete): authz.Any,
	},
	authz.RoleAdmin: {
		authz.Perm(ResourceMember, authz.Read):   authz.Any,
		authz.Perm(ResourceMember, authz.Create): authz.Any,
		authz.Perm(ResourceMember, authz.Update): authz.Any,
		authz.Perm(ResourceMember, authz.Delete): authz.Any,
	},
	authz.RoleMember: {
		authz.Perm(ResourceMember, authz.Read): authz.Any,
	},
	authz.RoleViewer: {
		authz.Perm(ResourceMember, authz.Read): authz.Any,
	},
}
