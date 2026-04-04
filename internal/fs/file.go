package fs

import (
	"context"
	"io"
	"os"
	"syscall"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
)

func (f *File) Attr(ctx context.Context, a *fuse.Attr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a.Inode = f.inode
	a.Mode = os.FileMode(f.mode)
	a.Size = uint64(len(f.data))

	return nil
}

func (f *File) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	return f, nil
}

func (f *File) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if req.Offset >= int64(len(f.data)) {
		resp.Data = []byte{}

		return nil
	}

	end := req.Offset + int64(req.Size)
	end = min(end, int64(len(f.data)))
	resp.Data = f.data[req.Offset:end]

	return nil
}

// Write writes data to the file, performing Copy-on-Write if the file
// exists only in the lower layer.
func (f *File) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Copy-on-Write: if the file doesn't exist in the upper layer yet,
	// copy it from the lower layer before modifying.
	if f.upperPath != "" {
		if _, err := os.Lstat(f.upperPath); os.IsNotExist(err) {
			if err := copyFile(f.lowerPath, f.upperPath); err != nil {
				return syscall.EIO
			}
		}
	}

	end := req.Offset + int64(len(req.Data))

	// Grow the buffer if needed
	if end > int64(len(f.data)) {
		newData := make([]byte, end)
		copy(newData, f.data)
		f.data = newData
	}

	copy(f.data[req.Offset:], req.Data)
	resp.Size = len(req.Data)

	return nil
}

// Setattr handles chmod, truncate, etc.
func (f *File) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if req.Valid.Mode() {
		f.mode = uint32(req.Mode)
	}

	if req.Valid.Size() {
		if req.Size < uint64(len(f.data)) {
			f.data = f.data[:req.Size]
		} else {
			newData := make([]byte, req.Size)
			copy(newData, f.data)
			f.data = newData
		}
	}

	resp.Attr.Inode = f.inode
	resp.Attr.Mode = os.FileMode(f.mode)
	resp.Attr.Size = uint64(len(f.data))

	return nil
}

// Flush is called when a file handle is closed. Persists in-memory data to disk.
func (f *File) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.upperPath == "" {
		return nil
	}

	return os.WriteFile(f.upperPath, f.data, os.FileMode(f.mode))
}

// Fsync persists in-memory data to disk on explicit sync.
func (f *File) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.upperPath == "" {
		return nil
	}

	return os.WriteFile(f.upperPath, f.data, os.FileMode(f.mode))
}

// copyFile copies src to dst, creating dst if it doesn't exist.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
