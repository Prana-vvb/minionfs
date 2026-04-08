package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bazil.org/fuse"
)

func newTestFile(t *testing.T, upper string, name string, data []byte) *File {
	t.Helper()
	upperPath := filepath.Join(upper, name)
	if err := os.WriteFile(upperPath, data, 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	return &File{
		inode:     nextInode(),
		mode:      0o644,
		upperPath: upperPath,
		codec:     PlainCodec{}, // Default to plain for basic lifecycle tests
	}
}

func TestFile_Attr(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	content := []byte("hello world")
	f := newTestFile(t, upper, "attr_test.txt", content)

	var a fuse.Attr
	if err := f.Attr(context.Background(), &a); err != nil {
		t.Fatalf("Attr error: %v", err)
	}
	if a.Size != uint64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), a.Size)
	}
	if a.Mode != os.FileMode(0o644) {
		t.Errorf("expected mode 0644, got %v", a.Mode)
	}
}

func TestFile_Read_Full(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	content := []byte("read me")
	f := newTestFile(t, upper, "read_full.txt", content)

	// FUSE Lifecycle: Open -> Read -> Release
	f.Open(context.Background(), &fuse.OpenRequest{Flags: fuse.OpenFlags(os.O_RDONLY)}, &fuse.OpenResponse{})
	defer f.Release(context.Background(), &fuse.ReleaseRequest{})

	req := &fuse.ReadRequest{Offset: 0, Size: len(content)}
	resp := &fuse.ReadResponse{}

	if err := f.Read(context.Background(), req, resp); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(resp.Data) != string(content) {
		t.Errorf("expected %q, got %q", content, resp.Data)
	}
}

func TestFile_Read_Partial(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	content := []byte("0123456789")
	f := newTestFile(t, upper, "read_partial.txt", content)

	f.Open(context.Background(), &fuse.OpenRequest{Flags: fuse.OpenFlags(os.O_RDONLY)}, &fuse.OpenResponse{})
	defer f.Release(context.Background(), &fuse.ReleaseRequest{})

	req := &fuse.ReadRequest{Offset: 3, Size: 4}
	resp := &fuse.ReadResponse{}

	if err := f.Read(context.Background(), req, resp); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(resp.Data) != "3456" {
		t.Errorf("expected %q, got %q", "3456", resp.Data)
	}
}

func TestFile_Read_PastEnd(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	content := []byte("short")
	f := newTestFile(t, upper, "read_past.txt", content)

	f.Open(context.Background(), &fuse.OpenRequest{Flags: fuse.OpenFlags(os.O_RDONLY)}, &fuse.OpenResponse{})
	defer f.Release(context.Background(), &fuse.ReleaseRequest{})

	req := &fuse.ReadRequest{Offset: int64(len(content)) + 10, Size: 5}
	resp := &fuse.ReadResponse{}

	if err := f.Read(context.Background(), req, resp); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Errorf("expected empty response, got %q", resp.Data)
	}
}

func TestFile_Write_OverwritesContent(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	f := newTestFile(t, upper, "write_test.txt", []byte("original"))

	f.Open(context.Background(), &fuse.OpenRequest{Flags: fuse.OpenFlags(os.O_RDWR)}, &fuse.OpenResponse{})
	defer f.Release(context.Background(), &fuse.ReleaseRequest{})

	req := &fuse.WriteRequest{Offset: 0, Data: []byte("replaced")}
	resp := &fuse.WriteResponse{}

	if err := f.Write(context.Background(), req, resp); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if resp.Size != len("replaced") {
		t.Errorf("expected write size %d, got %d", len("replaced"), resp.Size)
	}

	// Verify on disk instead of memory
	diskData, _ := os.ReadFile(f.upperPath)
	if string(diskData) != "replaced" {
		t.Errorf("disk data mismatch: expected %q, got %q", "replaced", diskData)
	}
}

func TestFile_Write_AppendsGrowsFile(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	f := newTestFile(t, upper, "write_grow.txt", []byte("AB"))

	f.Open(context.Background(), &fuse.OpenRequest{Flags: fuse.OpenFlags(os.O_RDWR)}, &fuse.OpenResponse{})
	defer f.Release(context.Background(), &fuse.ReleaseRequest{})

	req := &fuse.WriteRequest{Offset: 2, Data: []byte("CD")}
	resp := &fuse.WriteResponse{}

	if err := f.Write(context.Background(), req, resp); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	diskData, _ := os.ReadFile(f.upperPath)
	if string(diskData) != "ABCD" {
		t.Errorf("expected %q, got %q", "ABCD", diskData)
	}
}

func TestFile_Write_CopyOnWrite(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	lowerPath := filepath.Join(lower, "cow.txt")
	upperPath := filepath.Join(upper, "cow.txt")
	os.WriteFile(lowerPath, []byte("from lower"), 0o644)

	f := &File{
		inode:     nextInode(),
		mode:      0o644,
		upperPath: upperPath,
		lowerPath: lowerPath,
		codec:     PlainCodec{},
	}

	// Triggering CoW dynamically upon opening for Write
	f.Open(context.Background(), &fuse.OpenRequest{Flags: fuse.OpenFlags(os.O_RDWR)}, &fuse.OpenResponse{})
	defer f.Release(context.Background(), &fuse.ReleaseRequest{})

	req := &fuse.WriteRequest{Offset: 0, Data: []byte("modified ")}
	resp := &fuse.WriteResponse{}

	if err := f.Write(context.Background(), req, resp); err != nil {
		t.Fatalf("Write (CoW) error: %v", err)
	}

	if _, err := os.Stat(upperPath); err != nil {
		t.Errorf("expected upper copy to exist after CoW write: %v", err)
	}

	diskData, _ := os.ReadFile(upperPath)
	if string(diskData) != "modified r" {
		t.Errorf("expected modified CoW file to be 'modified r', got %q", diskData)
	}
}

func TestFile_Setattr_Mode(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	f := newTestFile(t, upper, "setattr_mode.txt", []byte("x"))
	req := &fuse.SetattrRequest{Valid: fuse.SetattrMode, Mode: 0o755}
	resp := &fuse.SetattrResponse{}

	if err := f.Setattr(context.Background(), req, resp); err != nil {
		t.Fatalf("Setattr error: %v", err)
	}
	if f.mode != uint32(0o755) {
		t.Errorf("expected mode 0755, got %o", f.mode)
	}

	stat, _ := os.Stat(f.upperPath)
	if stat.Mode().Perm() != 0o755 {
		t.Errorf("expected physical file mode 0755, got %v", stat.Mode().Perm())
	}
}

func TestFile_Setattr_TruncateShrink(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	f := newTestFile(t, upper, "setattr_trunc.txt", []byte("hello world"))
	req := &fuse.SetattrRequest{Valid: fuse.SetattrSize, Size: 5}
	resp := &fuse.SetattrResponse{}

	if err := f.Setattr(context.Background(), req, resp); err != nil {
		t.Fatalf("Setattr error: %v", err)
	}

	diskData, _ := os.ReadFile(f.upperPath)
	if string(diskData) != "hello" {
		t.Errorf("expected %q after truncate, got %q", "hello", diskData)
	}
}

func TestFile_Setattr_TruncateGrow(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	f := newTestFile(t, upper, "setattr_grow.txt", []byte("hi"))
	req := &fuse.SetattrRequest{Valid: fuse.SetattrSize, Size: 5}
	resp := &fuse.SetattrResponse{}

	if err := f.Setattr(context.Background(), req, resp); err != nil {
		t.Fatalf("Setattr error: %v", err)
	}

	stat, _ := os.Stat(f.upperPath)
	if stat.Size() != 5 {
		t.Errorf("expected length 5, got %d", stat.Size())
	}
}

func TestCopyAndEncodeChunked(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	src := filepath.Join(lower, "src.txt")
	dst := filepath.Join(upper, "dst.txt")
	os.WriteFile(src, []byte("copied content"), 0o644)

	if err := copyAndEncodeChunked(src, dst, PlainCodec{}); err != nil {
		t.Fatalf("copyAndEncodeChunked error: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read dst: %v", err)
	}
	if string(data) != "copied content" {
		t.Errorf("expected %q, got %q", "copied content", data)
	}
}

func TestCopyAndEncodeChunked_MissingSrc(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	if err := copyAndEncodeChunked("/nonexistent/src.txt", filepath.Join(upper, "dst.txt"), PlainCodec{}); err == nil {
		t.Error("expected error copying from nonexistent src")
	}
}

// ---- Reimagined Bug 4 Fix Test ----
// The original Bug 4 fix used a "dirty" flag to ensure clean files weren't flushed.
// In the chunking architecture, the underlying fd points to the lower layer for read-only
// access, so we naturally verify that an upper file is never created without a write.

func TestFile_CleanDoesNotWriteToUpper(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	lowerPath := filepath.Join(lower, "readonly.txt")
	upperPath := filepath.Join(upper, "readonly.txt")
	os.WriteFile(lowerPath, []byte("loaded from lower"), 0o644)

	f := &File{
		inode:     nextInode(),
		mode:      0o644,
		upperPath: upperPath,
		lowerPath: lowerPath,
		codec:     PlainCodec{},
	}

	// Open read-only (should resolve fd to lower layer)
	f.Open(context.Background(), &fuse.OpenRequest{Flags: fuse.OpenFlags(os.O_RDONLY)}, &fuse.OpenResponse{})
	
	// Simulate Flush/Fsync calls
	if err := f.Flush(context.Background(), &fuse.FlushRequest{}); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if err := f.Fsync(context.Background(), &fuse.FsyncRequest{}); err != nil {
		t.Fatalf("Fsync error: %v", err)
	}

	// Close
	f.Release(context.Background(), &fuse.ReleaseRequest{})

	// Validate upper file was never created
	if _, err := os.Stat(upperPath); !os.IsNotExist(err) {
		t.Error("Read-only lifecycle on a clean file must not create an upper-layer copy")
	}
}