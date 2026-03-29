package fs

import (
	"sync"
	"sync/atomic"

	"bazil.org/fuse/fs"
)

type FS struct {
	Debug    bool
	UpperDir string
	LowerDir string
}

var inodeCounter uint64 = 2

func nextInode() uint64 {
	return atomic.AddUint64(&inodeCounter, 1)
}

func (f *FS) Root() (fs.Node, error) {
	root := &Dir{
		inode:    1,
		upperDir: f.UpperDir,
		lowerDir: f.LowerDir,
		fs:       f,
	}

	return root, nil
}

type File struct {
	mu    sync.Mutex
	inode uint64
	data  []byte
	mode  uint32
}

type Dir struct {
	mu       sync.Mutex
	inode    uint64
	upperDir string
	lowerDir string
	fs       *FS
}
