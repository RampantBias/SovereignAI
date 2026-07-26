package requestidentity

import (
	"context"
	"testing"
)

func TestIdentityRoundTripsThroughContextWithoutSharingGroups(t *testing.T) {
	groups := []string{"contract-maintainers"}
	ctx := WithIdentity(context.Background(), Identity{Subject: "frank", Groups: groups})
	groups[0] = "tampered"

	identity, ok := FromContext(ctx)
	if !ok || identity.Subject != "frank" || !identity.InGroup("contract-maintainers") {
		t.Fatalf("unexpected identity: %#v, present=%t", identity, ok)
	}
	identity.Groups[0] = "tampered-again"
	second, _ := FromContext(ctx)
	if !second.InGroup("contract-maintainers") {
		t.Fatalf("context identity shared mutable group storage: %#v", second)
	}
}

func TestIdentityRequiresNonBlankSubjectAndExactGroup(t *testing.T) {
	if (Identity{Subject: "  "}).Valid() {
		t.Fatal("blank subject is valid")
	}
	identity := Identity{Subject: "frank", Groups: []string{"contract-maintainers-extra"}}
	if identity.InGroup("contract-maintainers") {
		t.Fatal("group check accepted a partial match")
	}
}
