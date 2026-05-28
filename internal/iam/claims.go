package iam

import "github.com/golang-jwt/jwt/v5"

// Claims is the JWT payload issued to authenticated users.
type Claims struct {
	UserID   string   `json:"user_id"`
	Email    string   `json:"email"`
	TenantID string   `json:"tenant_id"`
	Role     string   `json:"role"`   // "admin" | "developer" | "user"
	Groups   []string `json:"groups"`
	jwt.RegisteredClaims
}

// contextKey is an unexported type to avoid context key collisions.
type contextKey string

// UserContextKey is the context key used by the IAM middleware to store claims.
const UserContextKey contextKey = "user_context"
