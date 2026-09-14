package requestidentity

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type JWTConfig struct {
	PublicKey   ed25519.PublicKey
	Issuer      string
	Audience    string
	MaxLifetime time.Duration
}

type HumanClaims struct {
	Groups []string `json:"groups"`
	jwt.RegisteredClaims
}

func NewJWTUnaryServerInterceptor(config JWTConfig) (grpc.UnaryServerInterceptor, error) {
	if len(config.PublicKey) != ed25519.PublicKeySize || strings.TrimSpace(config.Issuer) == "" || strings.TrimSpace(config.Audience) == "" || config.MaxLifetime <= 0 {
		return nil, errors.New("invalid JWT authentication configuration")
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		identity, err := authenticate(ctx, config)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "valid bearer token required")
		}
		return handler(WithIdentity(ctx, identity), req)
	}, nil
}

func authenticate(ctx context.Context, config JWTConfig) (Identity, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 {
		return Identity{}, errors.New("authorization metadata must occur exactly once")
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return Identity{}, errors.New("malformed bearer token")
	}

	claims := &HumanClaims{}
	token, err := jwt.ParseWithClaims(parts[1], claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing method %q", token.Method.Alg())
		}
		return config.PublicKey, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}), jwt.WithIssuer(config.Issuer),
		jwt.WithAudience(config.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(30*time.Second))
	if err != nil || !token.Valid || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return Identity{}, errors.New("token validation failed")
	}
	lifetime := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
	if lifetime <= 0 || lifetime > config.MaxLifetime {
		return Identity{}, errors.New("token lifetime is invalid")
	}
	identity := Identity{Subject: strings.TrimSpace(claims.Subject), Groups: append([]string(nil), claims.Groups...)}
	if !identity.Valid() {
		return Identity{}, errors.New("token subject is required")
	}
	return identity, nil
}

func LoadEd25519PublicKey(path string) (ed25519.PublicKey, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(content)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("expected a PEM PUBLIC KEY")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("public key is not Ed25519")
	}
	return key, nil
}
