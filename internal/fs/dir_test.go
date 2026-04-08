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

// ---- isWhiteoutEntry ----

func TestIsWhiteoutEntry_True(t *testing.T) {
	if !isWhiteoutEntry(".wh.somefile.txt") {
		t.Error("expected isWhiteoutEntry to return true for .wh. prefixed name")
	}
}

func TestIsWhiteoutEntry_False(t *testing.T) {
	for _, name := range []string{"regular.txt", ".wh.", "wh.nope"} {
		if isWhiteoutEntry(name) {
			t.Errorf("isWhiteoutEntry(%q) should be false", name)
		}
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

// ---- removeWhiteout ----

func TestRemoveWhiteout_RemovesExisting(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "gone.txt"
	if err := createWhiteout(upper, name); err != nil {
		t.Fatalf("setup: createWhiteout failed: %v", err)
	}

	if err := removeWhiteout(upper, name); err != nil {
		t.Fatalf("removeWhiteout failed: %v", err)
	}

	if isWhiteout(upper, name) {
		t.Error("whiteout should be gone after removeWhiteout")
	}
}

func TestRemoveWhiteout_NoopWhenAbsent(t *testing.T) {
	_, upper, cleanup := setupOverlay(t)
	defer cleanup()

	// Must not error when there is nothing to remove.
	if err := removeWhiteout(upper, "never_existed.txt"); err != nil {
		t.Errorf("removeWhiteout on absent marker should not error, got: %v", err)
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

// TestLookup_SubdirInBothLayers verifies that when a subdirectory exists in
// both layers the returned Dir has both upperDir and lowerDir set correctly.
func TestLookup_SubdirInBothLayers(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	subName := "shared_sub"
	os.MkdirAll(filepath.Join(lower, subName), 0o755)
	os.MkdirAll(filepath.Join(upper, subName), 0o755)

	d := rootDir(lower, upper)
	node, err := d.Lookup(context.Background(), subName)
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	sub, ok := node.(*Dir)
	if !ok {
		t.Fatalf("expected *Dir, got %T", node)
	}
	if sub.upperDir != filepath.Join(upper, subName) {
		t.Errorf("upperDir mismatch: got %q, want %q", sub.upperDir, filepath.Join(upper, subName))
	}
	if sub.lowerDir != filepath.Join(lower, subName) {
		t.Errorf("lowerDir should be set when lower subdir exists, got %q", sub.lowerDir)
	}
}

// TestLookup_SubdirLowerOnly_CreatesUpperShadow verifies that when a
// subdirectory exists only in the lower layer, Lookup auto-creates the
// corresponding upper shadow directory so CoW operations can proceed.
func TestLookup_SubdirLowerOnly_CreatesUpperShadow(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	subName := "lower_sub"
	os.MkdirAll(filepath.Join(lower, subName), 0o755)

	d := rootDir(lower, upper)
	node, err := d.Lookup(context.Background(), subName)
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	sub, ok := node.(*Dir)
	if !ok {
		t.Fatalf("expected *Dir, got %T", node)
	}

	if _, err := os.Stat(filepath.Join(upper, subName)); err != nil {
		t.Errorf("Lookup should auto-create upper shadow dir: %v", err)
	}
	if sub.lowerDir != filepath.Join(lower, subName) {
		t.Errorf("lowerDir should point to lower subdir, got %q", sub.lowerDir)
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
		if isWhiteoutEntry(e.Name) {
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

// Bug 5: Mkdir must remove a stale whiteout for the same name so that the
// newly created directory is visible after remount.
func TestMkdir_ClearsStaleWhiteout(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "reborn"
	// Simulate: directory was previously deleted, leaving a whiteout.
	if err := createWhiteout(upper, name); err != nil {
		t.Fatalf("setup: createWhiteout: %v", err)
	}

	d := rootDir(lower, upper)
	req := &fuse.MkdirRequest{Name: name, Mode: os.ModeDir | 0o755}
	if _, err := d.Mkdir(context.Background(), req); err != nil {
		t.Fatalf("Mkdir error: %v", err)
	}

	// Whiteout must be gone — the new directory must be visible.
	if isWhiteout(upper, name) {
		t.Error("stale whiteout should have been removed by Mkdir")
	}

	// Directory must be visible in a fresh ReadDirAll.
	entries, err := d.ReadDirAll(context.Background())
	if err != nil {
		t.Fatalf("ReadDirAll error: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == name {
			found = true
		}
	}
	if !found {
		t.Errorf("recreated directory %q should be visible in listing", name)
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

// Bug 6: Create must remove a stale whiteout for the same name so that the
// newly created file is visible after remount.
func TestCreate_ClearsStaleWhiteout(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "zombie.txt"
	// Simulate: file was previously deleted from lower, leaving a whiteout.
	os.WriteFile(filepath.Join(lower, name), []byte("original"), 0o644)
	if err := createWhiteout(upper, name); err != nil {
		t.Fatalf("setup: createWhiteout: %v", err)
	}

	d := rootDir(lower, upper)
	req := &fuse.CreateRequest{Name: name, Mode: 0o644}
	resp := &fuse.CreateResponse{}
	if _, _, err := d.Create(context.Background(), req, resp); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	// Whiteout must be gone.
	if isWhiteout(upper, name) {
		t.Error("stale whiteout should have been removed by Create")
	}

	// File must be visible in a fresh ReadDirAll.
	entries, err := d.ReadDirAll(context.Background())
	if err != nil {
		t.Fatalf("ReadDirAll error: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == name {
			found = true
		}
	}
	if !found {
		t.Errorf("recreated file %q should be visible in listing", name)
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

// Bug fix: rmdir on a non-empty directory must return ENOTEMPTY.
// The old code used os.RemoveAll which silently nuked the directory.
func TestRemove_NonEmptyDirectory_ReturnsENOTEMPTY(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "notempty"
	os.MkdirAll(filepath.Join(upper, name), 0o755)
	// Put a file inside so the directory is non-empty.
	os.WriteFile(filepath.Join(upper, name, "child.txt"), []byte("x"), 0o644)

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: name, Dir: true}

	err := d.Remove(context.Background(), req)
	if err != syscall.ENOTEMPTY {
		t.Errorf("expected ENOTEMPTY for non-empty dir, got %v", err)
	}

	// Directory must still exist — nothing was deleted.
	if _, statErr := os.Stat(filepath.Join(upper, name)); statErr != nil {
		t.Error("non-empty directory should still exist after failed rmdir")
	}
}

// Bug fix: rmdir on an empty directory must succeed.
func TestRemove_EmptyDirectory_Succeeds(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "emptydir"
	os.MkdirAll(filepath.Join(upper, name), 0o755)

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: name, Dir: true}

	if err := d.Remove(context.Background(), req); err != nil {
		t.Fatalf("Remove on empty dir error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(upper, name)); !os.IsNotExist(err) {
		t.Error("directory should be removed from upper")
	}
}

// Bug 3: removing a directory that exists only in the lower layer must
// create a whiteout so the directory stays hidden after remount.
func TestRemove_LowerOnlyDirectory_CreatesWhiteout(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "lowerdir"
	os.MkdirAll(filepath.Join(lower, name), 0o755)

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: name, Dir: true}

	if err := d.Remove(context.Background(), req); err != nil {
		t.Fatalf("Remove of lower-only dir error: %v", err)
	}

	if !isWhiteout(upper, name) {
		t.Error("expected whiteout to be created for lower-only directory")
	}

	// Directory must be hidden in the merged view.
	entries, err := d.ReadDirAll(context.Background())
	if err != nil {
		t.Fatalf("ReadDirAll error: %v", err)
	}
	for _, e := range entries {
		if e.Name == name {
			t.Errorf("removed lower-only dir %q should not appear in listing", name)
		}
	}
}

// ---- Bug 7: Mkdir must not set lowerDir to a non-existent path ----

// TestMkdir_LowerDirEmptyWhenNoLowerSubdir verifies that a newly created
// directory gets an empty lowerDir when no matching subdirectory exists in
// the lower layer. Previously Mkdir set lowerDir unconditionally, which
// caused resolvePath inside the new Dir to produce incorrect lower paths.
func TestMkdir_LowerDirEmptyWhenNoLowerSubdir(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	d := rootDir(lower, upper)
	req := &fuse.MkdirRequest{Name: "brand_new", Mode: os.ModeDir | 0o755}

	node, err := d.Mkdir(context.Background(), req)
	if err != nil {
		t.Fatalf("Mkdir error: %v", err)
	}

	newDir, ok := node.(*Dir)
	if !ok {
		t.Fatalf("expected *Dir, got %T", node)
	}

	if newDir.lowerDir != "" {
		t.Errorf("lowerDir should be empty when lower subdir doesn't exist, got %q", newDir.lowerDir)
	}
}

// TestMkdir_LowerDirSetWhenLowerSubdirExists verifies that when a matching
// subdirectory already exists in the lower layer, Mkdir correctly sets lowerDir
// so that the merged view can see lower-layer contents inside the new dir.
func TestMkdir_LowerDirSetWhenLowerSubdirExists(t *testing.T) {
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "shared_subdir"
	lowerSub := filepath.Join(lower, name)
	if err := os.MkdirAll(lowerSub, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	d := rootDir(lower, upper)
	req := &fuse.MkdirRequest{Name: name, Mode: os.ModeDir | 0o755}

	node, err := d.Mkdir(context.Background(), req)
	if err != nil {
		t.Fatalf("Mkdir error: %v", err)
	}

	newDir, ok := node.(*Dir)
	if !ok {
		t.Fatalf("expected *Dir, got %T", node)
	}

	if newDir.lowerDir != lowerSub {
		t.Errorf("lowerDir should be %q, got %q", lowerSub, newDir.lowerDir)
	}
}

// ---- EIO error paths ----

// TestMkdir_UpperDirUnwritable_ReturnsEIO verifies that Mkdir returns EIO
// when the upper directory is not writable (e.g. due to permission error).
func TestMkdir_UpperDirUnwritable_ReturnsEIO(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: running as root bypasses permission checks")
	}
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	if err := os.Chmod(upper, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(upper, 0o755)

	d := rootDir(lower, upper)
	req := &fuse.MkdirRequest{Name: "fail", Mode: os.ModeDir | 0o755}
	_, err := d.Mkdir(context.Background(), req)
	if err != syscall.EIO {
		t.Errorf("expected EIO for unwritable upper dir, got %v", err)
	}
}

// TestCreate_UpperDirUnwritable_ReturnsEIO verifies that Create returns EIO
// when the upper directory is not writable.
func TestCreate_UpperDirUnwritable_ReturnsEIO(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: running as root bypasses permission checks")
	}
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	if err := os.Chmod(upper, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(upper, 0o755)

	d := rootDir(lower, upper)
	req := &fuse.CreateRequest{Name: "fail.txt", Mode: 0o644}
	_, _, err := d.Create(context.Background(), req, &fuse.CreateResponse{})
	if err != syscall.EIO {
		t.Errorf("expected EIO for unwritable upper dir, got %v", err)
	}
}

// TestRemove_LowerOnlyFile_WhiteoutFails_ReturnsEIO verifies that Remove
// returns EIO when the upper directory is not writable and createWhiteout
// cannot be created for a lower-only file.
func TestRemove_LowerOnlyFile_WhiteoutFails_ReturnsEIO(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: running as root bypasses permission checks")
	}
	lower, upper, cleanup := setupOverlay(t)
	defer cleanup()

	name := "lower_only.txt"
	if err := os.WriteFile(filepath.Join(lower, name), []byte("data"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := os.Chmod(upper, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(upper, 0o755)

	d := rootDir(lower, upper)
	req := &fuse.RemoveRequest{Name: name, Dir: false}
	if err := d.Remove(context.Background(), req); err != syscall.EIO {
		t.Errorf("expected EIO when whiteout creation fails, got %v", err)
	}
}
