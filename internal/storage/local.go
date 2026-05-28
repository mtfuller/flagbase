package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BucketAdapter defines the object-storage port used by flagbase.
// Local and cloud adapters implement this interface.
type BucketAdapter interface {
	PutObject(ctx context.Context, bucket, objectName string, reader io.Reader) error
	GetObject(ctx context.Context, bucket, objectName string) (io.ReadCloser, error)
	DeleteObject(ctx context.Context, bucket, objectName string) error
	ListObjects(ctx context.Context, bucket string) ([]string, error)
}

// LocalAdapter stores objects as files under basePath/<bucket>/<objectName>.
type LocalAdapter struct {
	basePath string
}

// NewLocalAdapter creates a LocalAdapter rooted at basePath.
func NewLocalAdapter(basePath string) *LocalAdapter {
	return &LocalAdapter{basePath: basePath}
}

func (a *LocalAdapter) PutObject(_ context.Context, bucket, objectName string, reader io.Reader) error {
	fullPath := filepath.Join(a.basePath, bucket, objectName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("creating object dir: %w", err)
	}
	f, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("creating object file: %w", err)
	}
	defer f.Close()
	_, err = io.Copy(f, reader)
	return err
}

// GetBasePath returns the root directory used by this adapter.
func (a *LocalAdapter) GetBasePath() string {
	return a.basePath
}

func (a *LocalAdapter) GetObject(_ context.Context, bucket, objectName string) (io.ReadCloser, error) {
	path := filepath.Join(a.basePath, bucket, objectName)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("object not found: %s/%s", bucket, objectName)
	}
	return f, err
}

func (a *LocalAdapter) DeleteObject(_ context.Context, bucket, objectName string) error {
	err := os.Remove(filepath.Join(a.basePath, bucket, objectName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (a *LocalAdapter) ListObjects(_ context.Context, bucket string) ([]string, error) {
	dir := filepath.Join(a.basePath, bucket)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// ListBuckets returns the names of all bucket directories under basePath.
func (a *LocalAdapter) ListBuckets(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(a.basePath)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// CreateBucket ensures the bucket directory exists.
func (a *LocalAdapter) CreateBucket(_ context.Context, bucket string) error {
	return os.MkdirAll(filepath.Join(a.basePath, bucket), 0o755)
}

// DeleteBucket removes a bucket directory and all its objects.
func (a *LocalAdapter) DeleteBucket(_ context.Context, bucket string) error {
	err := os.RemoveAll(filepath.Join(a.basePath, bucket))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

var _ BucketAdapter = (*LocalAdapter)(nil)
