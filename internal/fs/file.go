package fs

import (
	"bytes"
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

	// Dynamically determine physical file path
	var path string
	if f.upperPath != "" {
		if _, err := os.Stat(f.upperPath); err == nil {
			path = f.upperPath
		} else {
			path = f.lowerPath
		}
	} else {
		path = f.lowerPath
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// Auto-detect if lower layer has a different codec
	activeCodec := f.codec
	header := make([]byte, 5)
	n, _ := file.ReadAt(header, 0)
	if n == 5 && bytes.Equal(header[:4], magicPrefix) {
		if header[4] == typeGzip {
			activeCodec = GzipCodec{}
		}
	} else {
		if path == f.lowerPath {
			activeCodec = PlainCodec{}
		}
	}

	size, err := activeCodec.PlaintextSize(file)
	if err != nil {
		return err
	}
	a.Size = uint64(size)

	return nil
}

func (f *File) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var path string
	if f.upperPath != "" {
		if _, err := os.Stat(f.upperPath); err == nil {
			path = f.upperPath
		} else if f.lowerPath != "" {
			path = f.lowerPath
		}
	} else if f.lowerPath != "" {
		path = f.lowerPath
	}

	flags := int(req.Flags)

	// Proactive Copy-on-Write for write-requests on lower files
	if (flags&os.O_RDWR != 0 || flags&os.O_WRONLY != 0) && path == f.lowerPath {
		if err := copyAndEncodeChunked(f.lowerPath, f.upperPath, f.codec); err != nil {
			return nil, err
		}
		path = f.upperPath
		flags = os.O_RDWR
	}

	// Chunked AES requires O_RDWR for Read-Modify-Write functionality
	openFlags := flags
	if path == f.upperPath {
		openFlags = os.O_RDWR | os.O_CREATE
	}

	file, err := os.OpenFile(path, openFlags, os.FileMode(f.mode))
	if err != nil {
		return nil, err
	}
	f.fd = file
	return f, nil
}

func (f *File) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fd == nil {
		return syscall.EBADF
	}

	// Evaluate lower file codecs
	activeCodec := f.codec
	if f.fd.Name() == f.lowerPath {
		header := make([]byte, 5)
		n, _ := f.fd.ReadAt(header, 0)
		if n == 5 && bytes.Equal(header[:4], magicPrefix) {
			if header[4] == typeGzip {
				activeCodec = GzipCodec{}
			}
		} else {
			activeCodec = PlainCodec{}
		}
	}

	buf := make([]byte, req.Size)
	n, err := activeCodec.ReadAt(f.fd, buf, req.Offset)
	if err != nil && err != io.EOF {
		return err
	}
	resp.Data = buf[:n]
	return nil
}

func (f *File) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd == nil {
		return syscall.EBADF
	}

	// Reactive Copy-on-Write (in case file was opened O_RDONLY and suddenly written to)
	if f.fd.Name() == f.lowerPath {
		f.fd.Close()
		if err := copyAndEncodeChunked(f.lowerPath, f.upperPath, f.codec); err != nil {
			return err
		}
		file, err := os.OpenFile(f.upperPath, os.O_RDWR, os.FileMode(f.mode))
		if err != nil {
			return err
		}
		f.fd = file
	}

	// Resolved Conflict 1: Use chunking's WriteAt directly to disk. Discard in-memory f.data logic.
	n, err := f.codec.WriteAt(f.fd, req.Data, req.Offset)
	if err != nil {
		return err
	}

	resp.Size = n
	return nil
}

func (f *File) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if req.Valid.Mode() {
		f.mode = uint32(req.Mode)
		// Resolved Conflict 2: Keep the proactive chmod to the underlying file
		if f.fd != nil {
			f.fd.Chmod(os.FileMode(f.mode))
		} else if f.upperPath != "" {
			os.Chmod(f.upperPath, os.FileMode(f.mode))
		}
	}

	if req.Valid.Size() {
		if f.fd == nil {
			if f.upperPath == "" {
				return syscall.EBADF
			}
			file, err := os.OpenFile(f.upperPath, os.O_RDWR, os.FileMode(f.mode))
			if err != nil {
				return err
			}
			defer file.Close()
			if err := f.codec.Truncate(file, int64(req.Size)); err != nil {
				return err
			}
		} else {
			if err := f.codec.Truncate(f.fd, int64(req.Size)); err != nil {
				return err
			}
		}
		// Note: f.dirty removed here too, as chunking invalidates the need for it.
	}

	return nil
}

func (f *File) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd != nil {
		err := f.fd.Close()
		f.fd = nil
		return err
	}
	return nil
}

func (f *File) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd != nil {
		return f.fd.Sync()
	}
	return nil
}

func (f *File) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd != nil {
		return f.fd.Sync()
	}
	return nil
}

// Resolved Conflict 3: Keep copyAndEncodeChunked and discard persistLocked
// copyAndEncodeChunked streams blocks directly from the lower file to the upper layer without ballooning RAM
func copyAndEncodeChunked(srcPath, dstPath string, dstCodec FileCodec) error {
	srcF, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcF.Close()

	dstF, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer dstF.Close()

	header := make([]byte, 5)
	n, _ := srcF.ReadAt(header, 0)

	var srcCodec FileCodec = PlainCodec{}
	if n == 5 && bytes.Equal(header[:4], magicPrefix) {
		switch header[4] {
		case typeAES:
			srcCodec = dstCodec
		case typeGzip:
			srcCodec = GzipCodec{}
		}
	}

	buf := make([]byte, 4096)
	var offset int64 = 0
	for {
		n, err := srcCodec.ReadAt(srcF, buf, offset)
		if n > 0 {
			_, wErr := dstCodec.WriteAt(dstF, buf[:n], offset)
			if wErr != nil {
				return wErr
			}
			offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}
