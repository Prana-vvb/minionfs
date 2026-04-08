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
	Codec    FileCodec // nil means PlainCodec (no encoding)
}

// getCodec returns the configured codec, defaulting to PlainCodec.
func (f *FS) getCodec() FileCodec {
	if f.Codec == nil {
		return PlainCodec{}
	}
	return f.Codec
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
	mu        sync.Mutex
	inode     uint64
	data      []byte   // always plaintext in memory
	mode      uint32
	upperPath string   // where this file lives (or should live) in the upper layer
	lowerPath string   // where this file lives in the lower layer (empty if upper-only)
	codec     FileCodec
}

type Dir struct {
	mu       sync.Mutex
	inode    uint64
	upperDir string
	lowerDir string
	fs       *FS
}
