package requestidentity

import (
	"context"
	"strings"
)

// Identity is the authenticated human identity attached to an API request by
// a trusted transport boundary. Callers must never construct it from RPC
// payload fields.
type Identity struct {
	Subject string
	Groups  []string
}

type contextKey struct{}

// WithIdentity attaches a defensively copied identity to a request context.
// The P4.5 authentication interceptor will call this after verifying the
// request token; tests may use it to exercise authorization independently.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	identity.Groups = append([]string(nil), identity.Groups...)
	return context.WithValue(ctx, contextKey{}, identity)
}

// FromContext returns a defensive copy of the trusted request identity.
func FromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	if !ok {
		return Identity{}, false
	}
	identity.Groups = append([]string(nil), identity.Groups...)
	return identity, true
}

// Valid reports whether the verified identity has a non-blank subject.
func (i Identity) Valid() bool {
	return strings.TrimSpace(i.Subject) != ""
}

// InGroup performs an exact group membership check.
func (i Identity) InGroup(group string) bool {
	for _, candidate := range i.Groups {
		if candidate == group {
			return true
		}
	}
	return false
}
