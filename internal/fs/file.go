package fs

import (
	"context"
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
	a.Size = uint64(len(f.data)) // plaintext size — what the OS sees

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

// Write updates the in-memory buffer and immediately persists the encoded
// result to the upper layer. If the file exists only in the lower layer,
// Copy-on-Write is triggered first: the lower file is encoded and copied
// to the upper layer before the write proceeds.
func (f *File) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Copy-on-Write: upper file doesn't exist yet → copy lower → upper (encoded).
	if f.upperPath != "" {
		if _, err := os.Lstat(f.upperPath); os.IsNotExist(err) {
			if err := copyAndEncode(f.lowerPath, f.upperPath, f.codec); err != nil {
				return syscall.EIO
			}
		}
	}

	// Grow the buffer if the write extends beyond the current size.
	end := req.Offset + int64(len(req.Data))
	if end > int64(len(f.data)) {
		newData := make([]byte, end)
		copy(newData, f.data)
		f.data = newData
	}

	copy(f.data[req.Offset:], req.Data)
	resp.Size = len(req.Data)
	f.dirty = true

	// Persist encoded data to the upper layer immediately.
	if err := f.persistLocked(); err != nil {
		return syscall.EIO
	}

	return nil
}

// Setattr handles chmod, truncate, etc.
func (f *File) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if req.Valid.Mode() {
		f.mode = uint32(req.Mode)
		f.dirty = true
	}

	if req.Valid.Size() {
		if req.Size < uint64(len(f.data)) {
			f.data = f.data[:req.Size]
		} else {
			newData := make([]byte, req.Size)
			copy(newData, f.data)
			f.data = newData
		}
		f.dirty = true
	}

	resp.Attr.Inode = f.inode
	resp.Attr.Mode = os.FileMode(f.mode)
	resp.Attr.Size = uint64(len(f.data))

	return nil
}

// Flush is called when a file handle is closed. Persists encoded data to disk.
func (f *File) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.persistLocked()
}

// Fsync persists encoded data to disk on an explicit sync call.
func (f *File) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.persistLocked()
}

// persistLocked encodes f.data and writes it to upperPath.
// Must be called with f.mu held. No-op if the file has not been modified.
func (f *File) persistLocked() error {
	if f.upperPath == "" || !f.dirty {
		return nil
	}
	encoded, err := EncodeToDisk(f.data, f.codec)
	if err != nil {
		return err
	}
	return os.WriteFile(f.upperPath, encoded, os.FileMode(f.mode))
}

// copyAndEncode reads src (plaintext or encoded), decodes it, re-encodes it
// with the given codec, and writes the result to dst. Used for CoW.
func copyAndEncode(src, dst string, codec FileCodec) error {
	rawData, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	plaintext, err := DecodeFromDisk(rawData, codec)
	if err != nil {
		return err
	}
	encoded, err := EncodeToDisk(plaintext, codec)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, encoded, 0644)
}
