package fs

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
)

func (f *FS) DebugPrint(msg string, v ...any) {
	if f.Debug {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		logger.Info(msg, v...)
	}
}

func (d *Dir) resolvePath(name string) (fullPath string, layer string, err error) {
	upperPath := filepath.Join(d.upperDir, name)
	lowerPath := filepath.Join(d.lowerDir, name)

	// Check upper layer first
	if _, err := os.Lstat(upperPath); err == nil {
		return upperPath, "upper", nil
	}

	// Fall back to lower layer
	if _, err := os.Lstat(lowerPath); err == nil {
		return lowerPath, "lower", nil
	}

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
		return &Dir{
			inode:    nextInode(),
			upperDir: filepath.Join(d.upperDir, name),
			lowerDir: filepath.Join(d.lowerDir, name),
			fs:       d.fs,
		}, nil
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, syscall.EIO
	}

	return &File{
		inode: nextInode(),
		data:  data,
		mode:  uint32(info.Mode()),
	}, nil
}

func (d *Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	d.fs.DebugPrint("READDIR", "inode", d.inode)

	d.mu.Lock()
	defer d.mu.Unlock()

	// Use a map to merge entries. upper layer wins on name collision
	seen := make(map[string]fuse.DirentType)

	// Read upper layer
	if entries, err := os.ReadDir(d.upperDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				seen[e.Name()] = fuse.DT_Dir
			} else {
				seen[e.Name()] = fuse.DT_File
			}
		}
	}

	// Read lower layer. skip names already in upper
	if entries, err := os.ReadDir(d.lowerDir); err == nil {
		for _, e := range entries {
			if _, exists := seen[e.Name()]; !exists {
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

	f := &File{
		inode: nextInode(),
		data:  []byte{},
		mode:  uint32(req.Mode),
		atime: time.Now(),
		ctime: time.Now(),
		mtime: time.Now(),
	}
	d.Nodes[req.Name] = f

	return f, f, nil
}

func (d *Dir) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	d.fs.DebugPrint(
		"REMOVE",
		"ID", req.ID,
		"Is this a directory?", req.Dir,
		"Removing file/dir", req.Name,
		"NodeID", req.ID,
		"Request PID", req.Pid,
	)

	d.mu.Lock()
	defer d.mu.Unlock()

	upperPath := filepath.Join(d.upperDir, req.Name)

	if _, err := os.Lstat(upperPath); err == nil {
		return os.Remove(upperPath)
	}

	return syscall.ENOENT
}