package service

// ResourceMember is the resource type of member checks: they are type-level
// (tenant:<id>#can_<action>_member), decided by the base model of
// core/authz/rebac (base.yml). Rules involving owners (admins can't touch or
// grant owner, the last owner stays) are enforced in MemberService on top.
const ResourceMember = "member"
