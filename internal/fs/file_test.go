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
		data:      append([]byte{}, data...),
		mode:      0o644,
		upperPath: upperPath,
	}
}

// ---- Attr ----

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

// ---- Read ----

func TestFile_Read_Full(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	content := []byte("read me")
	f := newTestFile(t, upper, "read_full.txt", content)

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

	req := &fuse.ReadRequest{Offset: int64(len(content)) + 10, Size: 5}
	resp := &fuse.ReadResponse{}

	if err := f.Read(context.Background(), req, resp); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Errorf("expected empty response, got %q", resp.Data)
	}
}

// ---- Write ----

func TestFile_Write_OverwritesContent(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	f := newTestFile(t, upper, "write_test.txt", []byte("original"))

	req := &fuse.WriteRequest{Offset: 0, Data: []byte("replaced")}
	resp := &fuse.WriteResponse{}

	if err := f.Write(context.Background(), req, resp); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if resp.Size != len("replaced") {
		t.Errorf("expected write size %d, got %d", len("replaced"), resp.Size)
	}
	if string(f.data) != "replaced" {
		t.Errorf("in-memory data mismatch: %q", f.data)
	}
}

func TestFile_Write_AppendsGrowsBuffer(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	f := newTestFile(t, upper, "write_grow.txt", []byte("AB"))

	req := &fuse.WriteRequest{Offset: 2, Data: []byte("CD")}
	resp := &fuse.WriteResponse{}

	if err := f.Write(context.Background(), req, resp); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if string(f.data) != "ABCD" {
		t.Errorf("expected %q, got %q", "ABCD", f.data)
	}
}

func TestFile_Write_PersistsToDisk(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	upperPath := filepath.Join(upper, "persist.txt")
	f := &File{
		inode:     nextInode(),
		data:      []byte("old"),
		mode:      0o644,
		upperPath: upperPath,
	}
	os.WriteFile(upperPath, []byte("old"), 0o644)

	req := &fuse.WriteRequest{Offset: 0, Data: []byte("new")}
	resp := &fuse.WriteResponse{}

	if err := f.Write(context.Background(), req, resp); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	diskData, err := os.ReadFile(upperPath)
	if err != nil {
		t.Fatalf("failed to read back file: %v", err)
	}
	if string(diskData) != "new" {
		t.Errorf("disk data: expected %q, got %q", "new", diskData)
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
		data:      []byte("from lower"),
		mode:      0o644,
		upperPath: upperPath,
		lowerPath: lowerPath,
	}

	req := &fuse.WriteRequest{Offset: 0, Data: []byte("modified")}
	resp := &fuse.WriteResponse{}

	if err := f.Write(context.Background(), req, resp); err != nil {
		t.Fatalf("Write (CoW) error: %v", err)
	}

	if _, err := os.Stat(upperPath); err != nil {
		t.Errorf("expected upper copy to exist after CoW write: %v", err)
	}
}

// ---- Setattr ----

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
	if string(f.data) != "hello" {
		t.Errorf("expected %q after truncate, got %q", "hello", f.data)
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
	if uint64(len(f.data)) != 5 {
		t.Errorf("expected length 5, got %d", len(f.data))
	}
}

// ---- Flush / Fsync ----

func TestFile_Flush_PersistsToDisk(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	upperPath := filepath.Join(upper, "flush.txt")
	f := &File{
		inode:     nextInode(),
		data:      []byte("flushed"),
		mode:      0o644,
		upperPath: upperPath,
	}

	if err := f.Flush(context.Background(), &fuse.FlushRequest{}); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	data, err := os.ReadFile(upperPath)
	if err != nil {
		t.Fatalf("failed to read flushed file: %v", err)
	}
	if string(data) != "flushed" {
		t.Errorf("expected %q, got %q", "flushed", data)
	}
}

func TestFile_Flush_NoUpperPath_IsNoop(t *testing.T) {
	f := &File{inode: nextInode(), data: []byte("x"), mode: 0o644}
	if err := f.Flush(context.Background(), &fuse.FlushRequest{}); err != nil {
		t.Errorf("Flush with no upperPath should not error, got: %v", err)
	}
}

func TestFile_Fsync_PersistsToDisk(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	upperPath := filepath.Join(upper, "fsync.txt")
	f := &File{
		inode:     nextInode(),
		data:      []byte("synced"),
		mode:      0o644,
		upperPath: upperPath,
	}

	if err := f.Fsync(context.Background(), &fuse.FsyncRequest{}); err != nil {
		t.Fatalf("Fsync error: %v", err)
	}

	data, err := os.ReadFile(upperPath)
	if err != nil {
		t.Fatalf("failed to read fsynced file: %v", err)
	}
	if string(data) != "synced" {
		t.Errorf("expected %q, got %q", "synced", data)
	}
}

// ---- copyFile ----

func TestCopyFile(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	src := filepath.Join(lower, "src.txt")
	dst := filepath.Join(upper, "dst.txt")
	os.WriteFile(src, []byte("copied content"), 0o644)

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile error: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read dst: %v", err)
	}
	if string(data) != "copied content" {
		t.Errorf("expected %q, got %q", "copied content", data)
	}
}

func TestCopyFile_MissingSrc(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	if err := copyFile("/nonexistent/src.txt", filepath.Join(upper, "dst.txt")); err == nil {
		t.Error("expected error copying from nonexistent src")
	}
}
