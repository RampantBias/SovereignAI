package requestidentity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestJWTInterceptorAttachesVerifiedIdentity(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	interceptor, err := NewJWTUnaryServerInterceptor(JWTConfig{
		PublicKey: publicKey, Issuer: "issuer", Audience: "api", MaxLifetime: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token := signedToken(t, privateKey, HumanClaims{
		Groups: []string{"contract-maintainers"},
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "issuer", Subject: "frank", Audience: jwt.ClaimStrings{"api"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		},
	})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	_, err = interceptor(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
		identity, ok := FromContext(ctx)
		if !ok || identity.Subject != "frank" || !identity.InGroup("contract-maintainers") {
			t.Fatalf("identity = %#v, %t", identity, ok)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJWTInterceptorRejectsMissingAndOverlongTokens(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	interceptor, err := NewJWTUnaryServerInterceptor(JWTConfig{
		PublicKey: publicKey, Issuer: "issuer", Audience: "api", MaxLifetime: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := func(context.Context, any) (any, error) {
		t.Fatal("handler called for unauthenticated request")
		return nil, nil
	}
	if _, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token code = %s, want Unauthenticated", status.Code(err))
	}

	now := time.Now().UTC()
	token := signedToken(t, privateKey, HumanClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "issuer", Subject: "frank", Audience: jwt.ClaimStrings{"api"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(16 * time.Minute)),
		},
	})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	if _, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("overlong token code = %s, want Unauthenticated", status.Code(err))
	}
}

func signedToken(t *testing.T, privateKey ed25519.PrivateKey, claims HumanClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
