package acpbackend

import (
	"testing"

	"github.com/bberka/agentpassage/internal/backend"
)

func TestPermissionOption(t *testing.T) {
	type opt = backend.PermissionOption
	for name, tc := range map[string]struct {
		opts          []opt
		approve, deny string
	}{
		"one-time beats standing": {
			[]opt{{ID: "a", Kind: "allow_always"}, {ID: "b", Kind: "allow_once"}, {ID: "c", Kind: "reject_once"}}, "b", "c",
		},
		"only standing": {
			[]opt{{ID: "a", Kind: "allow_always"}, {ID: "c", Kind: "reject_always"}}, "a", "c",
		},
		"disallow is not allow": {
			[]opt{{ID: "disallow", Name: "Disallow"}, {ID: "x", Name: "Allow this time"}}, "x", "disallow",
		},
		"always by name only": {
			[]opt{{ID: "allow_all", Name: "Always allow"}, {ID: "ok", Name: "Yes, proceed"}, {ID: "n", Name: "No"}}, "ok", "n",
		},
		"nothing recognised": {
			[]opt{{ID: "1", Name: "Edit the command"}, {ID: "2", Name: "Don't allow"}}, "", "2",
		},
		"no way to refuse": {
			[]opt{{ID: "a", Kind: "allow_once"}}, "a", "",
		},
	} {
		if got := permissionOption(tc.opts, true); got != tc.approve {
			t.Errorf("%s: approve picked %q, want %q", name, got, tc.approve)
		}
		if got := permissionOption(tc.opts, false); got != tc.deny {
			t.Errorf("%s: deny picked %q, want %q", name, got, tc.deny)
		}
	}
}
