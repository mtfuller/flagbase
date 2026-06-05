package packages

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mtfuller/flagbase/internal/storage"
)

const packagesBucket = "packages"

// Package is a registry entry for an npm package approved for use in JS functions.
type Package struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Status      string     `json:"status"` // pending | bundling | available | error
	Error       string     `json:"error,omitempty"`
	BundleSize  int64      `json:"bundle_size"`
	RequestedBy string     `json:"requested_by"`
	ApprovedBy  string     `json:"approved_by,omitempty"`
	RequestedAt time.Time  `json:"requested_at"`
	ApprovedAt  *time.Time `json:"approved_at,omitempty"`
}

// Store manages the package registry lifecycle.
type Store struct {
	db      *sql.DB
	storage *storage.LocalAdapter
}

func NewStore(db *sql.DB, store *storage.LocalAdapter) *Store {
	return &Store{db: db, storage: store}
}

// Request creates a pending package entry requested by a user.
func (s *Store) Request(ctx context.Context, name, version, requestedBy string) (*Package, error) {
	if name == "" || version == "" {
		return nil, fmt.Errorf("name and version are required")
	}
	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO packages (id, name, version, status, requested_by)
		VALUES (?, ?, ?, 'pending', ?)`,
		id, name, version, requestedBy)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil, fmt.Errorf("package %s@%s is already in the registry", name, version)
		}
		return nil, fmt.Errorf("inserting package: %w", err)
	}
	return s.Get(ctx, id)
}

// Approve fetches and bundles an npm package, marking it available for use.
// Fetch is done from jsDelivr CDN using the package's npm main entry.
func (s *Store) Approve(ctx context.Context, id, approvedBy string) (*Package, error) {
	pkg, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		return nil, fmt.Errorf("package not found")
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE packages SET status='bundling', approved_by=?, approved_at=CURRENT_TIMESTAMP
		WHERE id=?`, approvedBy, id)
	if err != nil {
		return nil, fmt.Errorf("updating package status: %w", err)
	}

	go func() {
		bgCtx := context.Background()
		bundle, fetchErr := fetchBundle(pkg.Name, pkg.Version)
		if fetchErr != nil {
			_, _ = s.db.ExecContext(bgCtx, `
				UPDATE packages SET status='error', error=? WHERE id=?`,
				fetchErr.Error(), id)
			return
		}

		if err := s.storage.PutObject(bgCtx, packagesBucket, id+".js", strings.NewReader(bundle)); err != nil {
			_, _ = s.db.ExecContext(bgCtx, `
				UPDATE packages SET status='error', error=? WHERE id=?`,
				"storing bundle: "+err.Error(), id)
			return
		}

		_, _ = s.db.ExecContext(bgCtx, `
			UPDATE packages SET status='available', bundle_size=? WHERE id=?`,
			len(bundle), id)

		// Best-effort: fetch and store TypeScript definitions for editor IntelliSense.
		if dts, typesErr := fetchTypes(pkg.Name, pkg.Version); typesErr == nil {
			_ = s.storage.PutObject(bgCtx, packagesBucket, id+".d.ts", strings.NewReader(dts))
		}
	}()

	return s.Get(ctx, id)
}

// Delete removes a package entry and its stored bundle and type definitions.
func (s *Store) Delete(ctx context.Context, id string) error {
	_ = s.storage.DeleteObject(ctx, packagesBucket, id+".js")
	_ = s.storage.DeleteObject(ctx, packagesBucket, id+".d.ts")
	_, err := s.db.ExecContext(ctx, `DELETE FROM packages WHERE id=?`, id)
	return err
}

// List returns all packages.
func (s *Store) List(ctx context.Context) ([]Package, error) {
	return s.query(ctx, `SELECT id, name, version, status, error, bundle_size, requested_by, approved_by, requested_at, approved_at
		FROM packages ORDER BY requested_at DESC`, nil)
}

// ListAvailable returns only approved, available packages.
func (s *Store) ListAvailable(ctx context.Context) ([]Package, error) {
	return s.query(ctx, `SELECT id, name, version, status, error, bundle_size, requested_by, approved_by, requested_at, approved_at
		FROM packages WHERE status='available' ORDER BY name ASC`, nil)
}

// Get returns a single package by ID.
func (s *Store) Get(ctx context.Context, id string) (*Package, error) {
	pkgs, err := s.query(ctx, `SELECT id, name, version, status, error, bundle_size, requested_by, approved_by, requested_at, approved_at
		FROM packages WHERE id=?`, []interface{}{id})
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, nil
	}
	return &pkgs[0], nil
}

// LoadBundle reads the stored JavaScript bundle for a package.
func (s *Store) LoadBundle(ctx context.Context, id string) (string, error) {
	rc, err := s.storage.GetObject(ctx, packagesBucket, id+".js")
	if err != nil {
		return "", fmt.Errorf("loading bundle: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("reading bundle: %w", err)
	}
	return string(data), nil
}

// LoadTypes reads the stored TypeScript declarations for a package.
// Returns an empty string (no error) when no types have been stored.
func (s *Store) LoadTypes(ctx context.Context, id string) (string, error) {
	rc, err := s.storage.GetObject(ctx, packagesBucket, id+".d.ts")
	if err != nil {
		if strings.Contains(err.Error(), "object not found") {
			return "", nil
		}
		return "", fmt.Errorf("loading types: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("reading types: %w", err)
	}
	return string(data), nil
}

func (s *Store) query(ctx context.Context, q string, args []interface{}) ([]Package, error) {
	var rows *sql.Rows
	var err error
	if args != nil {
		rows, err = s.db.QueryContext(ctx, q, args...)
	} else {
		rows, err = s.db.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pkgs []Package
	for rows.Next() {
		var p Package
		var errText sql.NullString
		var approvedBy sql.NullString
		var approvedAt sql.NullTime
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Version, &p.Status, &errText,
			&p.BundleSize, &p.RequestedBy, &approvedBy, &p.RequestedAt, &approvedAt,
		); err != nil {
			return nil, err
		}
		if errText.Valid {
			p.Error = errText.String
		}
		if approvedBy.Valid {
			p.ApprovedBy = approvedBy.String
		}
		if approvedAt.Valid {
			p.ApprovedAt = &approvedAt.Time
		}
		pkgs = append(pkgs, p)
	}
	if pkgs == nil {
		pkgs = []Package{}
	}
	return pkgs, nil
}

// fetchBundle fetches a UMD/CJS build from jsDelivr CDN.
// It tries the minified path first, then the unminified package root.
func fetchBundle(name, version string) (string, error) {
	// jsDelivr resolves to the package's browser/main field automatically.
	url := fmt.Sprintf("https://cdn.jsdelivr.net/npm/%s@%s", name, version)
	body, err := httpGet(url)
	if err != nil {
		return "", fmt.Errorf("fetching %s@%s: %w", name, version, err)
	}
	if len(body) == 0 {
		return "", fmt.Errorf("empty bundle for %s@%s", name, version)
	}
	return body, nil
}

// fetchTypes tries to retrieve TypeScript declarations for an npm package.
// Strategy: (1) check package.json types/typings field, (2) try index.d.ts at
// the package root, (3) fall back to @types/<name> from DefinitelyTyped.
func fetchTypes(name, version string) (string, error) {
	pkgJSON, err := httpGet(fmt.Sprintf("https://cdn.jsdelivr.net/npm/%s@%s/package.json", name, version))
	if err == nil && pkgJSON != "" {
		var meta struct {
			Types   string `json:"types"`
			Typings string `json:"typings"`
		}
		if json.Unmarshal([]byte(pkgJSON), &meta) == nil {
			typesPath := meta.Types
			if typesPath == "" {
				typesPath = meta.Typings
			}
			if typesPath != "" {
				typesPath = strings.TrimPrefix(typesPath, "./")
				dts, err := httpGet(fmt.Sprintf("https://cdn.jsdelivr.net/npm/%s@%s/%s", name, version, typesPath))
				if err == nil && strings.Contains(dts, "declare") {
					return dts, nil
				}
			}
		}
	}

	// Try a bare index.d.ts at the package root.
	dts, err := httpGet(fmt.Sprintf("https://cdn.jsdelivr.net/npm/%s@%s/index.d.ts", name, version))
	if err == nil && strings.Contains(dts, "declare") {
		return dts, nil
	}

	// Fall back to DefinitelyTyped (@types/<name>).
	dts, err = httpGet(fmt.Sprintf("https://cdn.jsdelivr.net/npm/@types/%s/index.d.ts", name))
	if err == nil && strings.Contains(dts, "declare") {
		return dts, nil
	}

	return "", fmt.Errorf("no type definitions found for %s@%s", name, version)
}

func httpGet(url string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
