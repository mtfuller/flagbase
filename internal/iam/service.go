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
	ErrGroupNotFound   = errors.New("group not found")
	ErrGroupExists     = errors.New("group already exists")
	ErrInvalidRole     = errors.New("invalid role: must be admin, developer, or user")
)

var validRoles = map[string]bool{"admin": true, "developer": true, "user": true}

// User is a registered account in flagbase.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	TenantID  string    `json:"tenant_id"`
	Groups    []string  `json:"groups"`
	CreatedAt time.Time `json:"created_at"`
}

// Group is a named set of users used for flag targeting.
type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
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
	groups, err := s.userGroupNames(u.ID)
	if err != nil {
		return "", fmt.Errorf("loading user groups: %w", err)
	}
	u.Groups = groups
	return s.IssueToken(&u)
}

// IssueToken creates a signed JWT for the given user.
func (s *Service) IssueToken(u *User) (string, error) {
	groups := u.Groups
	if groups == nil {
		groups = []string{}
	}
	claims := Claims{
		UserID:   u.ID,
		Email:    u.Email,
		TenantID: u.TenantID,
		Role:     u.Role,
		Groups:   groups,
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

// CountAdmins returns the number of users with the "admin" role.
func (s *Service) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n)
	return n, err
}

// ListUsers returns all users ordered by creation time, including group names.
func (s *Service) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(
		`SELECT id, email, role, tenant_id, created_at FROM users ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	var users []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.TenantID, &u.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, &u)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // release the connection before nested queries

	for _, u := range users {
		groups, err := s.userGroupNames(u.ID)
		if err != nil {
			return nil, err
		}
		u.Groups = groups
	}
	return users, nil
}

// DeleteUser removes a user by ID. Returns ErrUserNotFound if no row was deleted.
func (s *Service) DeleteUser(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// UpdateUserRole changes a user's role. Validates that the role is one of the three valid values.
func (s *Service) UpdateUserRole(userID, role string) error {
	if !validRoles[role] {
		return ErrInvalidRole
	}
	res, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// CreateGroup creates a new named group.
func (s *Service) CreateGroup(name, description string) (*Group, error) {
	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating group id: %w", err)
	}
	var g Group
	err = s.db.QueryRow(
		`INSERT INTO groups (id, name, description) VALUES (?, ?, ?) RETURNING id, name, description, created_at`,
		id, name, description,
	).Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrGroupExists
		}
		return nil, err
	}
	return &g, nil
}

// ListGroups returns all groups with their member counts.
func (s *Service) ListGroups() ([]*Group, error) {
	rows, err := s.db.Query(`
		SELECT g.id, g.name, g.description, g.created_at, COUNT(ug.user_id) AS member_count
		FROM groups g
		LEFT JOIN user_groups ug ON ug.group_id = g.id
		GROUP BY g.id
		ORDER BY g.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []*Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.MemberCount); err != nil {
			return nil, err
		}
		groups = append(groups, &g)
	}
	return groups, rows.Err()
}

// DeleteGroup removes a group and all its memberships.
func (s *Service) DeleteGroup(id string) error {
	res, err := s.db.Exec(`DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// AddUserToGroup adds a user to a group. Silently succeeds if already a member.
func (s *Service) AddUserToGroup(userID, groupID string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO user_groups (user_id, group_id) VALUES (?, ?)`, userID, groupID,
	)
	return err
}

// RemoveUserFromGroup removes a user from a group.
func (s *Service) RemoveUserFromGroup(userID, groupID string) error {
	_, err := s.db.Exec(`DELETE FROM user_groups WHERE user_id = ? AND group_id = ?`, userID, groupID)
	return err
}

// ListGroupMembers returns the users belonging to a group.
func (s *Service) ListGroupMembers(groupID string) ([]*User, error) {
	rows, err := s.db.Query(`
		SELECT u.id, u.email, u.role, u.tenant_id, u.created_at
		FROM users u
		JOIN user_groups ug ON ug.user_id = u.id
		WHERE ug.group_id = ?
		ORDER BY u.created_at DESC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.TenantID, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Groups = []string{}
		users = append(users, &u)
	}
	return users, rows.Err()
}

// userGroupNames returns the group names for a given user (used internally).
func (s *Service) userGroupNames(userID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT g.name FROM groups g
		JOIN user_groups ug ON ug.group_id = g.id
		WHERE ug.user_id = ?
		ORDER BY g.name
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if names == nil {
		names = []string{}
	}
	return names, rows.Err()
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
