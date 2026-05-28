package iam

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrUserNotFound    = errors.New("user not found")
	ErrInvalidPassword = errors.New("invalid password")
	ErrUserExists      = errors.New("user already exists")
)

// User is a registered account in flagbase.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	TenantID  string    `json:"tenant_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Service handles authentication and token lifecycle.
type Service struct {
	db        *sql.DB
	jwtSecret []byte
	tokenTTL  time.Duration
}

func NewService(db *sql.DB, jwtSecret string, tokenTTL time.Duration) *Service {
	return &Service{
		db:        db,
		jwtSecret: []byte(jwtSecret),
		tokenTTL:  tokenTTL,
	}
}

// Register creates a new user account.
func (s *Service) Register(email, password, role, tenantID string) (*User, error) {
	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating user id: %w", err)
	}
	hash := hashPassword(password)

	var createdAt time.Time
	err = s.db.QueryRow(
		`INSERT INTO users (id, email, password, role, tenant_id) VALUES (?, ?, ?, ?, ?)
         RETURNING id, email, role, tenant_id, created_at`,
		id, email, hash, role, tenantID,
	).Scan(&id, &email, &role, &tenantID, &createdAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return &User{ID: id, Email: email, Role: role, TenantID: tenantID, CreatedAt: createdAt}, nil
}

// Login validates credentials and returns a signed JWT.
func (s *Service) Login(email, password string) (string, error) {
	var u User
	var hash string
	err := s.db.QueryRow(
		`SELECT id, email, role, tenant_id, password FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.Email, &u.Role, &u.TenantID, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserNotFound
	}
	if err != nil {
		return "", err
	}
	if hash != hashPassword(password) {
		return "", ErrInvalidPassword
	}
	return s.IssueToken(&u)
}

// IssueToken creates a signed JWT for the given user.
func (s *Service) IssueToken(u *User) (string, error) {
	claims := Claims{
		UserID:   u.ID,
		Email:    u.Email,
		TenantID: u.TenantID,
		Role:     u.Role,
		Groups:   []string{},
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.tokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(s.jwtSecret)
}

// ValidateToken parses and validates a JWT, returning the embedded claims.
func (s *Service) ValidateToken(token string) (*Claims, error) {
	token = strings.TrimPrefix(token, "Bearer ")
	tok, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := tok.Claims.(*Claims)
	if !ok || !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

func hashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
