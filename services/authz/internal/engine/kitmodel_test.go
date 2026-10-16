package engine

import (
	"context"
	"os"
	"testing"
)

// The platform model (deploy/authz/model.fga) must grant exactly what the RBAC
// policy of the note service (templates/service/internal/service/policy.go) does:
//
//	owner, admin  everything
//	member        create, read all, update/delete own
//	viewer        read all
func TestKitModelMatchesNotePolicy(t *testing.T) {
	dsl, err := os.ReadFile("../../../../deploy/authz/model.fga")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseDSL(string(dsl))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModel("kit", p)
	if err != nil {
		t.Fatal(err)
	}

	r := &memReader{}
	for _, s := range []string{
		"tenant:acme#owner@user:olivia",
		"tenant:acme#admin@user:adam",
		"tenant:acme#member@user:mia",
		"tenant:acme#member@user:max",
		"tenant:acme#viewer@user:vera",
		"tenant:globex#owner@user:gina",
		"note:1#tenant@tenant:acme",
		"note:1#owner@user:mia",
		// vera created note:2 while a member, then was demoted to viewer
		"note:2#tenant@tenant:acme",
		"note:2#owner@user:vera",
	} {
		tp := tup(t, s)
		if err := m.ValidateTuple(tp); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		r.tuples = append(r.tuples, tp)
	}
	c := NewChecker(m, r)

	ask := func(obj, rel, user string) bool {
		o, _ := ParseObject("o", obj)
		ok, err := c.Check(context.Background(), o, rel, Subject{Type: "user", ID: user})
		if err != nil {
			t.Fatalf("%s#%s@%s: %v", obj, rel, user, err)
		}
		return ok
	}

	users := []string{"olivia", "adam", "mia", "max", "vera", "gina"}
	expect := map[string]map[string][]string{ // object#relation → allowed users
		"tenant:acme": {
			"can_create_note": {"olivia", "adam", "mia", "max"},
			"can_read_note":   {"olivia", "adam", "mia", "max", "vera"},
		},
		"note:1": {
			"can_read":   {"olivia", "adam", "mia", "max", "vera"},
			"can_update": {"olivia", "adam", "mia"}, // mia owns it
			"can_delete": {"olivia", "adam", "mia"},
		},
		"note:2": {
			"can_update": {"olivia", "adam"}, // vera owns it but is only a viewer now
			"can_delete": {"olivia", "adam"},
		},
	}
	for obj, rels := range expect {
		for rel, allowed := range rels {
			set := map[string]bool{}
			for _, u := range allowed {
				set[u] = true
			}
			for _, u := range users {
				if got := ask(obj, rel, u); got != set[u] {
					t.Errorf("%s#%s@user:%s = %v, want %v", obj, rel, u, got, set[u])
				}
			}
		}
	}
}
