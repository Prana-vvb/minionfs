package fs

import (
	"os"
	"path/filepath"
	"testing"
)

// setupOverlay creates a temporary lower and upper directory pair for testing.
// Returns (lowerDir, upperDir, cleanup).
func setupOverlay(t *testing.T) (string, string, func()) {
	t.Helper()

	tmp := t.TempDir()
	lower := filepath.Join(tmp, "lower")
	upper := filepath.Join(tmp, "upper")

	for _, d := range []string{lower, upper} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}

	return lower, upper, func() { os.RemoveAll(tmp) }
}

func newTestFS(lower, upper string) *FS {
	return &FS{
		Debug:    false,
		LowerDir: lower,
		UpperDir: upper,
	}
}

// rootDir returns the root Dir node for a test FS.
func rootDir(lower, upper string) *Dir {
	f := newTestFS(lower, upper)
	return &Dir{
		inode:    1,
		upperDir: upper,
		lowerDir: lower,
		fs:       f,
	}
}

// ---- inode counter ----

func TestNextInodeMonotonicallyIncreases(t *testing.T) {
	a := nextInode()
	b := nextInode()
	if b <= a {
		t.Errorf("expected b (%d) > a (%d)", b, a)
	}
}

// ---- Root() ----

func TestFSRoot(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	f := newTestFS(lower, upper)
	node, err := f.Root()
	if err != nil {
		t.Fatalf("Root() returned error: %v", err)
	}
	if node == nil {
		t.Fatal("Root() returned nil node")
	}
}
