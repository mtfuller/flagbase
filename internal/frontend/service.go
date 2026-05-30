package frontend

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mtfuller/flagbase/internal/storage"
)

const bucket = "frontends"

// Frontend represents a named frontend app with a set of uploadable versions.
type Frontend struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Slug            string    `json:"slug"`
	Description     string    `json:"description"`
	ActiveVersionID string    `json:"active_version_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// FrontendVersion represents a snapshot of uploaded files for a frontend.
type FrontendVersion struct {
	ID          string    `json:"id"`
	FrontendID  string    `json:"frontend_id"`
	Label       string    `json:"label"`
	Description string    `json:"description"`
	FileCount   int       `json:"file_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// Service manages frontends and their versions.
type Service struct {
	db    *sql.DB
	store *storage.LocalAdapter
}

// NewService creates a new frontend service.
func NewService(db *sql.DB, store *storage.LocalAdapter) *Service {
	return &Service{db: db, store: store}
}

// Create inserts a new frontend record.
func (s *Service) Create(ctx context.Context, name, slug, description string) (*Frontend, error) {
	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}
	f := &Frontend{
		ID:          id,
		Name:        name,
		Slug:        slug,
		Description: description,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO frontends (id, name, slug, description)
		VALUES (?, ?, ?, ?)`,
		f.ID, f.Name, f.Slug, f.Description)
	if err != nil {
		return nil, fmt.Errorf("inserting frontend: %w", err)
	}
	return s.Get(ctx, id)
}

// List returns all frontends ordered by creation date descending.
func (s *Service) List(ctx context.Context) ([]Frontend, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, slug, description, COALESCE(active_version_id,''), created_at, updated_at
		FROM frontends ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Frontend
	for rows.Next() {
		var f Frontend
		if err := rows.Scan(&f.ID, &f.Name, &f.Slug, &f.Description, &f.ActiveVersionID, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if out == nil {
		out = []Frontend{}
	}
	return out, nil
}

// Get returns a single frontend by ID.
func (s *Service) Get(ctx context.Context, id string) (*Frontend, error) {
	var f Frontend
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, description, COALESCE(active_version_id,''), created_at, updated_at
		FROM frontends WHERE id = ?`, id).
		Scan(&f.ID, &f.Name, &f.Slug, &f.Description, &f.ActiveVersionID, &f.CreatedAt, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &f, err
}

// GetBySlug returns a single frontend by slug.
func (s *Service) GetBySlug(ctx context.Context, slug string) (*Frontend, error) {
	var f Frontend
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, description, COALESCE(active_version_id,''), created_at, updated_at
		FROM frontends WHERE slug = ?`, slug).
		Scan(&f.ID, &f.Name, &f.Slug, &f.Description, &f.ActiveVersionID, &f.CreatedAt, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &f, err
}

// Update modifies the name and description of a frontend.
func (s *Service) Update(ctx context.Context, id, name, description string) (*Frontend, error) {
	_, err := s.db.ExecContext(ctx, `
		UPDATE frontends SET name = ?, description = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, name, description, id)
	if err != nil {
		return nil, fmt.Errorf("updating frontend: %w", err)
	}
	return s.Get(ctx, id)
}

// Delete removes a frontend and all its versions and stored files.
func (s *Service) Delete(ctx context.Context, id string) error {
	versions, err := s.ListVersions(ctx, id)
	if err != nil {
		return fmt.Errorf("listing versions for deletion: %w", err)
	}
	for _, v := range versions {
		if err := s.deleteVersionFiles(ctx, id, v.ID); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM frontends WHERE id = ?`, id)
	return err
}

// ListVersions returns all versions for a frontend ordered by creation date descending.
func (s *Service) ListVersions(ctx context.Context, frontendID string) ([]FrontendVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, frontend_id, label, description, file_count, created_at
		FROM frontend_versions WHERE frontend_id = ? ORDER BY created_at DESC`, frontendID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FrontendVersion
	for rows.Next() {
		var v FrontendVersion
		if err := rows.Scan(&v.ID, &v.FrontendID, &v.Label, &v.Description, &v.FileCount, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if out == nil {
		out = []FrontendVersion{}
	}
	return out, nil
}

// GetVersion returns a single version by ID.
func (s *Service) GetVersion(ctx context.Context, versionID string) (*FrontendVersion, error) {
	var v FrontendVersion
	err := s.db.QueryRowContext(ctx, `
		SELECT id, frontend_id, label, description, file_count, created_at
		FROM frontend_versions WHERE id = ?`, versionID).
		Scan(&v.ID, &v.FrontendID, &v.Label, &v.Description, &v.FileCount, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &v, err
}

// CreateVersion extracts a ZIP archive and stores all files under the new version.
func (s *Service) CreateVersion(ctx context.Context, frontendID, label, description string, zipData []byte) (*FrontendVersion, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("parsing zip: %w", err)
	}

	versionID, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating version id: %w", err)
	}

	fileCount := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Guard against zip-slip attacks.
		cleanName := filepath.Clean(filepath.ToSlash(f.Name))
		if strings.HasPrefix(cleanName, "..") {
			return nil, fmt.Errorf("invalid path in zip: %s", f.Name)
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening zip entry %s: %w", f.Name, err)
		}
		objectName := frontendID + "/" + versionID + "/" + cleanName
		putErr := s.store.PutObject(ctx, bucket, objectName, rc)
		rc.Close()
		if putErr != nil {
			return nil, fmt.Errorf("storing file %s: %w", f.Name, putErr)
		}
		fileCount++
	}

	if fileCount == 0 {
		return nil, fmt.Errorf("zip archive contains no files")
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO frontend_versions (id, frontend_id, label, description, file_count)
		VALUES (?, ?, ?, ?, ?)`,
		versionID, frontendID, label, description, fileCount)
	if err != nil {
		return nil, fmt.Errorf("inserting version: %w", err)
	}

	return s.GetVersion(ctx, versionID)
}

// ActivateVersion sets the active version for a frontend.
func (s *Service) ActivateVersion(ctx context.Context, frontendID, versionID string) (*Frontend, error) {
	_, err := s.db.ExecContext(ctx, `
		UPDATE frontends SET active_version_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, versionID, frontendID)
	if err != nil {
		return nil, fmt.Errorf("activating version: %w", err)
	}
	return s.Get(ctx, frontendID)
}

// DeleteVersion removes a version and its files. If the version is active, the frontend's
// active_version_id is cleared.
func (s *Service) DeleteVersion(ctx context.Context, frontendID, versionID string) error {
	if err := s.deleteVersionFiles(ctx, frontendID, versionID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM frontend_versions WHERE id = ?`, versionID)
	if err != nil {
		return fmt.Errorf("deleting version record: %w", err)
	}
	// Clear active_version_id if it pointed to the deleted version.
	_, err = s.db.ExecContext(ctx, `
		UPDATE frontends SET active_version_id = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND active_version_id = ?`, frontendID, versionID)
	return err
}

// VersionDir returns the filesystem directory where a version's files are stored.
func (s *Service) VersionDir(frontendID, versionID string) string {
	return filepath.Join(s.store.GetBasePath(), bucket, frontendID, versionID)
}

// deleteVersionFiles removes all stored files for a version from the storage layer.
func (s *Service) deleteVersionFiles(ctx context.Context, frontendID, versionID string) error {
	dir := s.VersionDir(frontendID, versionID)
	err := os.RemoveAll(dir)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
