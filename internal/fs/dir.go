package fs

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
)

const whiteoutPrefix = ".wh."

func (f *FS) DebugPrint(msg string, v ...any) {
	if f.Debug {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		logger.Info(msg, v...)
	}
}

// isWhiteout reports whether a whiteout marker exists in upperDir for name.
func isWhiteout(upperDir, name string) bool {
	whiteoutPath := filepath.Join(upperDir, whiteoutPrefix+name)
	_, err := os.Lstat(whiteoutPath)
	return err == nil
}

// isWhiteoutEntry reports whether a directory entry name is itself a whiteout
// marker file (i.e. starts with the whiteout prefix). Used in ReadDirAll to
// avoid duplicating the prefix check inline.
func isWhiteoutEntry(name string) bool {
	return len(name) > len(whiteoutPrefix) && name[:len(whiteoutPrefix)] == whiteoutPrefix
}

// createWhiteout creates a zero-byte whiteout marker file in the upper layer
// to hide a file/directory that exists only in the lower layer.
func createWhiteout(upperDir, name string) error {
	whiteoutPath := filepath.Join(upperDir, whiteoutPrefix+name)
	f, err := os.Create(whiteoutPath)
	if err != nil {
		return err
	}
	return f.Close()
}

// removeWhiteout deletes a whiteout marker for name from upperDir, if one
// exists. Called when a file or directory is being recreated after deletion
// so that the new entry is no longer hidden.
func removeWhiteout(upperDir, name string) error {
	whiteoutPath := filepath.Join(upperDir, whiteoutPrefix+name)
	err := os.Remove(whiteoutPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *Dir) resolvePath(name string) (fullPath string, layer string, err error) {
	// Step 1: Check for whiteout in upper layer first.
	// Only relevant when a lower layer exists — whiteouts exist to hide lower-layer
	// files. Skip this check entirely for upper-only dirs (e.g. created via Mkdir).
	if d.lowerDir != "" && isWhiteout(d.upperDir, name) {
		return "", "", syscall.ENOENT
	}

	upperPath := filepath.Join(d.upperDir, name)
	lowerPath := filepath.Join(d.lowerDir, name)

	// Step 2: Check upper layer.
	if _, statErr := os.Lstat(upperPath); statErr == nil {
		return upperPath, "upper", nil
	}

	// Step 3: Check lower layer (only if lowerDir is set).
	if d.lowerDir != "" {
		if _, statErr := os.Lstat(lowerPath); statErr == nil {
			return lowerPath, "lower", nil
		}
	}

	// Step 4: File not found in either layer.
	return "", "", syscall.ENOENT
}

func (d *Dir) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Inode = d.inode
	a.Mode = os.ModeDir | 0o755

	return nil
}

func (d *Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	d.fs.DebugPrint("LOOKUP", "fetching", name)

	d.mu.Lock()
	defer d.mu.Unlock()

	fullPath, layer, err := d.resolvePath(name)
	if err != nil {
		return nil, syscall.ENOENT
	}

	d.fs.DebugPrint("LOOKUP", "found", name, "in layer", layer, "at", fullPath)

	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil, syscall.ENOENT
	}

	if info.IsDir() {
		upperSub := filepath.Join(d.upperDir, name)
		lowerSub := filepath.Join(d.lowerDir, name)

		// Only set lowerDir if the subdirectory actually exists in the lower layer.
		if _, err := os.Lstat(lowerSub); err != nil {
			lowerSub = ""
		}

		// Ensure the upper subdir exists so that CoW writes and Flush don't fail.
		if _, err := os.Lstat(upperSub); os.IsNotExist(err) {
			if mkErr := os.MkdirAll(upperSub, 0o755); mkErr != nil {
				return nil, syscall.EIO
			}
		}

		return &Dir{
			inode:    nextInode(),
			upperDir: upperSub,
			lowerDir: lowerSub,
			fs:       d.fs,
		}, nil
	}

	rawData, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, syscall.EIO
	}
	plaintext, err := DecodeFromDisk(rawData, d.fs.getCodec())
	if err != nil {
		return nil, syscall.EIO
	}

	return &File{
		inode:     nextInode(),
		data:      plaintext,
		mode:      uint32(info.Mode()),
		upperPath: filepath.Join(d.upperDir, name),
		lowerPath: filepath.Join(d.lowerDir, name),
		codec:     d.fs.getCodec(),
	}, nil
}

func (d *Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	d.fs.DebugPrint("READDIR", "inode", d.inode)

	d.mu.Lock()
	defer d.mu.Unlock()

	seen := make(map[string]fuse.DirentType)


	// Read upper layer — skip whiteout marker files themselves using the
	// shared isWhiteoutEntry helper (avoids duplicating the prefix logic).
	if entries, err := os.ReadDir(d.upperDir); err == nil {
		for _, e := range entries {
			if isWhiteoutEntry(e.Name()) {
				continue
			}
			if e.IsDir() {
				seen[e.Name()] = fuse.DT_Dir
			} else {
				seen[e.Name()] = fuse.DT_File
			}
		}
	}

	// Read lower layer — skip names already in upper, and skip whited-out files.
	if d.lowerDir != "" {
		if entries, err := os.ReadDir(d.lowerDir); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), whiteoutPrefix) {
					continue
				}

				// Skip if already seen from upper layer.
				if _, exists := seen[e.Name()]; exists {
					continue
				}

				// Skip if a whiteout exists for this file in the upper layer.
				if isWhiteout(d.upperDir, e.Name()) {
					continue
				}

				if e.IsDir() {
					seen[e.Name()] = fuse.DT_Dir
				} else {
					seen[e.Name()] = fuse.DT_File
				}
			}
		}
	}

	var dirents []fuse.Dirent
	for name, dt := range seen {
		dirents = append(dirents, fuse.Dirent{Name: name, Type: dt})
	}

	return dirents, nil
}

func (d *Dir) Mkdir(ctx context.Context, req *fuse.MkdirRequest) (fs.Node, error) {
	d.fs.DebugPrint(
		"MKDIR",
		"ID", req.ID,
		"Creating directory", req.Name,
		"NodeID", req.Node,
		"With mode", req.Mode,
		"Request PID", req.Pid,
	)

	d.mu.Lock()
	defer d.mu.Unlock()

	newUpperPath := filepath.Join(d.upperDir, req.Name)

	if err := os.Mkdir(newUpperPath, req.Mode); err != nil {
		if os.IsExist(err) {
			return nil, syscall.EEXIST
		}
		return nil, syscall.EIO
	}

	if err := removeWhiteout(d.upperDir, req.Name); err != nil {
		// Non-fatal: log and continue — the new dir exists in upper and takes
		// precedence over any lower entry anyway; the stale whiteout only
		// matters across remounts.
		d.fs.DebugPrint("MKDIR", "warning: could not remove stale whiteout for", req.Name)
	}

	newDir := &Dir{
		inode:    nextInode(),
		upperDir: newUpperPath,
		lowerDir: filepath.Join(d.lowerDir, req.Name),
		fs:       d.fs,
	}

	return newDir, nil
}

func (d *Dir) Create(ctx context.Context, req *fuse.CreateRequest, resp *fuse.CreateResponse) (fs.Node, fs.Handle, error) {
	d.fs.DebugPrint(
		"CREATE",
		"ID", req.ID,
		"Creating file", req.Name,
		"NodeID", req.Node,
		"With mode", req.Mode,
		"Request PID", req.Pid,
		"Access mode", req.Flags,
	)

	d.mu.Lock()
	defer d.mu.Unlock()

	upperPath := filepath.Join(d.upperDir, req.Name)

	osFile, err := os.OpenFile(upperPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, req.Mode)
	if err != nil {
		return nil, nil, syscall.EIO
	}
	osFile.Close()

	if err := removeWhiteout(d.upperDir, req.Name); err != nil {
		d.fs.DebugPrint("CREATE", "warning: could not remove stale whiteout for", req.Name)
	}

	f := &File{
		inode:     nextInode(),
		data:      []byte{},
		mode:      uint32(req.Mode),
		upperPath: upperPath,
		codec:     d.fs.getCodec(),
	}

	return f, f, nil
}

// Remove handles file and directory deletion with whiteout support.
//
// Cases:
//  1. File exists in upper layer only   → delete it directly.
//  2. File exists in both layers        → delete upper copy, create whiteout to hide lower copy.
//  3. File exists in lower layer only   → create a whiteout marker in the upper layer to hide it.
//  4. File does not exist in any layer  → return ENOENT.
func (d *Dir) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	d.fs.DebugPrint(
		"REMOVE",
		"ID", req.ID,
		"Is this a directory?", req.Dir,
		"Removing file/dir", req.Name,
		"NodeID", req.Node,
		"Request PID", req.Pid,
	)

	d.mu.Lock()
	defer d.mu.Unlock()

	upperPath := filepath.Join(d.upperDir, req.Name)
	lowerPath := filepath.Join(d.lowerDir, req.Name)

	inUpper := false
	if _, err := os.Lstat(upperPath); err == nil {
		inUpper = true
	}

	inLower := false
	if d.lowerDir != "" {
		if _, err := os.Lstat(lowerPath); err == nil {
			inLower = true
		}
	}

	if !inUpper && !inLower {
		return syscall.ENOENT
	}

	// Remove the upper copy if it exists.
	if inUpper {
		if err := os.Remove(upperPath); err != nil {
			// Translate the OS error to the appropriate errno. On Linux,
			// removing a non-empty directory returns syscall.ENOTEMPTY.
			if isNotEmpty(err) {
				return syscall.ENOTEMPTY
			}
			return syscall.EIO
		}
	}

	// If the entry also existed in the lower layer, create a whiteout so it
	// stays hidden in the merged view — for both files and directories.
	if inLower {
		if err := createWhiteout(d.upperDir, req.Name); err != nil {
			return syscall.EIO
		}
		d.fs.DebugPrint("REMOVE", "created whiteout for", req.Name)
	}

	return nil
}

// isNotEmpty reports whether err corresponds to an ENOTEMPTY errno, handling
// the *os.PathError wrapper that os.Remove returns.
func isNotEmpty(err error) bool {
	if err == syscall.ENOTEMPTY {
		return true
	}
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err == syscall.ENOTEMPTY
	}
	return false
}
