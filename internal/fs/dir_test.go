package fs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"bazil.org/fuse"
)

// ---- isWhiteout ----

func TestIsWhiteout_Present(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "ghost.txt"
	wh := filepath.Join(upper, whiteoutPrefix+name)
	if err := os.WriteFile(wh, nil, 0o644); err != nil {
		t.Fatalf("could not create whiteout: %v", err)
	}

	if !isWhiteout(upper, name) {
		t.Error("expected isWhiteout to return true")
	}
}

func TestIsWhiteout_Absent(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	if isWhiteout(upper, "no_such_file.txt") {
		t.Error("expected isWhiteout to return false")
	}
}

// ---- createWhiteout ----

func TestCreateWhiteout(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "deleted.txt"
	if err := createWhiteout(upper, name); err != nil {
		t.Fatalf("createWhiteout failed: %v", err)
	}

	if !isWhiteout(upper, name) {
		t.Error("whiteout file not found after createWhiteout")
	}
}

// ---- resolvePath ----

func TestResolvePath_UpperWins(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "shared.txt"
	os.WriteFile(filepath.Join(lower, name), []byte("lower"), 0o644)
	os.WriteFile(filepath.Join(upper, name), []byte("upper"), 0o644)

	d := rootDir(lower, upper)
	path, layer, err := d.resolvePath(name)
	if err != nil {
		t.Fatalf("resolvePath error: %v", err)
	}
	if layer != "upper" {
		t.Errorf("expected upper layer, got %s", layer)
	}
	if path != filepath.Join(upper, name) {
		t.Errorf("unexpected path: %s", path)
	}
}

func TestResolvePath_LowerFallback(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "lower_only.txt"
	os.WriteFile(filepath.Join(lower, name), []byte("lower"), 0o644)

	d := rootDir(lower, upper)
	_, layer, err := d.resolvePath(name)
	if err != nil {
		t.Fatalf("resolvePath error: %v", err)
	}
	if layer != "lower" {
		t.Errorf("expected lower layer, got %s", layer)
	}
}

func TestResolvePath_WhiteoutHidesLower(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "hidden.txt"
	os.WriteFile(filepath.Join(lower, name), []byte("lower"), 0o644)
	os.WriteFile(filepath.Join(upper, whiteoutPrefix+name), nil, 0o644)

	d := rootDir(lower, upper)
	_, _, err := d.resolvePath(name)
	if err != syscall.ENOENT {
		t.Errorf("expected ENOENT due to whiteout, got %v", err)
	}
}

func TestResolvePath_NotFound(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	d := rootDir(lower, upper)
	_, _, err := d.resolvePath("nonexistent.txt")
	if err != syscall.ENOENT {
		t.Errorf("expected ENOENT, got %v", err)
	}
}

// ---- Lookup ----

func TestLookup_FileInUpper(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "upper_file.txt"
	os.WriteFile(filepath.Join(upper, name), []byte("hello"), 0o644)

	d := rootDir(lower, upper)
	node, err := d.Lookup(context.Background(), name)
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if node == nil {
		t.Fatal("Lookup returned nil node")
	}
}

func TestLookup_FileInLower(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "lower_file.txt"
	os.WriteFile(filepath.Join(lower, name), []byte("from lower"), 0o644)

	d := rootDir(lower, upper)
	node, err := d.Lookup(context.Background(), name)
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if node == nil {
		t.Fatal("Lookup returned nil node")
	}
}

func TestLookup_WhitedOutFile(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "gone.txt"
	os.WriteFile(filepath.Join(lower, name), []byte("should be hidden"), 0o644)
	os.WriteFile(filepath.Join(upper, whiteoutPrefix+name), nil, 0o644)

	d := rootDir(lower, upper)
	_, err := d.Lookup(context.Background(), name)
	if err == nil {
		t.Error("expected error for whited-out file, got nil")
	}
}

func TestLookup_Subdirectory(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	subName := "subdir"
	os.MkdirAll(filepath.Join(upper, subName), 0o755)

	d := rootDir(lower, upper)
	node, err := d.Lookup(context.Background(), subName)
	if err != nil {
		t.Fatalf("Lookup on subdir error: %v", err)
	}
	if _, ok := node.(*Dir); !ok {
		t.Errorf("expected *Dir node, got %T", node)
	}
}

// ---- ReadDirAll ----

func TestReadDirAll_MergesLayers(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	os.WriteFile(filepath.Join(lower, "lower_only.txt"), []byte("l"), 0o644)
	os.WriteFile(filepath.Join(upper, "upper_only.txt"), []byte("u"), 0o644)
	os.WriteFile(filepath.Join(lower, "shared.txt"), []byte("lower"), 0o644)
	os.WriteFile(filepath.Join(upper, "shared.txt"), []byte("upper"), 0o644)

	d := rootDir(lower, upper)
	entries, err := d.ReadDirAll(context.Background())
	if err != nil {
		t.Fatalf("ReadDirAll error: %v", err)
	}

	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}

	for _, want := range []string{"lower_only.txt", "upper_only.txt", "shared.txt"} {
		if !names[want] {
			t.Errorf("expected %q in merged listing, got: %v", want, names)
		}
	}
	// shared.txt must appear exactly once
	count := 0
	for _, e := range entries {
		if e.Name == "shared.txt" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("shared.txt should appear once, got %d", count)
	}
}

func TestReadDirAll_HidesWhitedOut(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "removed.txt"
	os.WriteFile(filepath.Join(lower, name), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(upper, whiteoutPrefix+name), nil, 0o644)

	d := rootDir(lower, upper)
	entries, err := d.ReadDirAll(context.Background())
	if err != nil {
		t.Fatalf("ReadDirAll error: %v", err)
	}

	for _, e := range entries {
		if e.Name == name {
			t.Errorf("whited-out file %q should not appear in listing", name)
		}
	}
}

func TestReadDirAll_DoesNotExposeWhiteoutMarkers(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "something.txt"
	os.WriteFile(filepath.Join(upper, whiteoutPrefix+name), nil, 0o644)

	d := rootDir(lower, upper)
	entries, err := d.ReadDirAll(context.Background())
	if err != nil {
		t.Fatalf("ReadDirAll error: %v", err)
	}

	for _, e := range entries {
		if len(e.Name) >= len(whiteoutPrefix) && e.Name[:len(whiteoutPrefix)] == whiteoutPrefix {
			t.Errorf("whiteout marker %q should not appear in listing", e.Name)
		}
	}
}

// ---- Mkdir ----

func TestMkdir_CreatesInUpper(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	d := rootDir(lower, upper)
	req := &fuse.MkdirRequest{Name: "newdir", Mode: os.ModeDir | 0o755}

	node, err := d.Mkdir(context.Background(), req)
	if err != nil {
		t.Fatalf("Mkdir error: %v", err)
	}
	if _, ok := node.(*Dir); !ok {
		t.Errorf("expected *Dir, got %T", node)
	}

	if _, err := os.Stat(filepath.Join(upper, "newdir")); err != nil {
		t.Errorf("expected dir to exist in upper: %v", err)
	}
}

func TestMkdir_DuplicateReturnsEEXIST(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	os.Mkdir(filepath.Join(upper, "existing"), 0o755)

	d := rootDir(lower, upper)
	req := &fuse.MkdirRequest{Name: "existing", Mode: os.ModeDir | 0o755}

	_, err := d.Mkdir(context.Background(), req)
	if err != syscall.EEXIST {
		t.Errorf("expected EEXIST, got %v", err)
	}
}

// ---- Create ----

func TestCreate_NewFileInUpper(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	d := rootDir(lower, upper)
	req := &fuse.CreateRequest{Name: "created.txt", Mode: 0o644}
	resp := &fuse.CreateResponse{}

	node, handle, err := d.Create(context.Background(), req, resp)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if node == nil || handle == nil {
		t.Fatal("Create returned nil node or handle")
	}

	if _, err := os.Stat(filepath.Join(upper, "created.txt")); err != nil {
		t.Errorf("created file not found in upper: %v", err)
	}
}

// ---- Remove ----

func TestRemove_UpperOnlyFile(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "upper_file.txt"
	os.WriteFile(filepath.Join(upper, name), []byte("data"), 0o644)

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: name, Dir: false}

	if err := d.Remove(context.Background(), req); err != nil {
		t.Fatalf("Remove error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(upper, name)); !os.IsNotExist(err) {
		t.Error("file should be gone from upper after Remove")
	}
	// No whiteout needed — file never existed in lower
	if isWhiteout(upper, name) {
		t.Error("unexpected whiteout for upper-only file")
	}
}

func TestRemove_LowerOnlyFile_CreatesWhiteout(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "lower_file.txt"
	os.WriteFile(filepath.Join(lower, name), []byte("data"), 0o644)

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: name, Dir: false}

	if err := d.Remove(context.Background(), req); err != nil {
		t.Fatalf("Remove error: %v", err)
	}

	if !isWhiteout(upper, name) {
		t.Error("expected whiteout to be created for lower-only file")
	}
}

func TestRemove_BothLayers_DeletesUpperAndCreatesWhiteout(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "shared.txt"
	os.WriteFile(filepath.Join(lower, name), []byte("lower"), 0o644)
	os.WriteFile(filepath.Join(upper, name), []byte("upper"), 0o644)

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: name, Dir: false}

	if err := d.Remove(context.Background(), req); err != nil {
		t.Fatalf("Remove error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(upper, name)); !os.IsNotExist(err) {
		t.Error("upper copy should be deleted")
	}
	if !isWhiteout(upper, name) {
		t.Error("whiteout should be created since lower copy exists")
	}
}

func TestRemove_NotFound_ReturnsENOENT(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: "ghost.txt", Dir: false}

	if err := d.Remove(context.Background(), req); err != syscall.ENOENT {
		t.Errorf("expected ENOENT, got %v", err)
	}
}

func TestRemove_Directory(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "mydir"
	os.MkdirAll(filepath.Join(upper, name), 0o755)

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: name, Dir: true}

	if err := d.Remove(context.Background(), req); err != nil {
		t.Fatalf("Remove dir error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(upper, name)); !os.IsNotExist(err) {
		t.Error("directory should be removed from upper")
	}
}
