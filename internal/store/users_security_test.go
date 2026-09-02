package store

import (
	"errors"
	"testing"
)

func TestOIDCLinkDecisionNeverLinksByUsername(t *testing.T) {
	if create, err := oidcLinkDecision(0, 42, true); create || !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("username collision must be rejected: create=%v err=%v", create, err)
	}
	if create, err := oidcLinkDecision(0, 0, false); create || !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled auto-create must reject: create=%v err=%v", create, err)
	}
	if create, err := oidcLinkDecision(0, 0, true); !create || err != nil {
		t.Fatalf("new subject should be created: create=%v err=%v", create, err)
	}
}
