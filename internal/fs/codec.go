package fs

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
)

var magicPrefix = []byte{0x4D, 0x46, 0x53, 0x00}

const (
	magicLen  = 5
	typePlain = byte(0x00)
	typeAES   = byte(0x01)
	typeGzip  = byte(0x02)
)

// FileCodec enables O(1) random access reads and writes at the disk boundary.
type FileCodec interface {
	ReadAt(f *os.File, p []byte, off int64) (int, error)
	WriteAt(f *os.File, p []byte, off int64) (int, error)
	Truncate(f *os.File, plainSize int64) error
	PlaintextSize(f *os.File) (int64, error)
}

type PlainCodec struct{}

func (PlainCodec) ReadAt(f *os.File, p []byte, off int64) (int, error)  { return f.ReadAt(p, off) }
func (PlainCodec) WriteAt(f *os.File, p []byte, off int64) (int, error) { return f.WriteAt(p, off) }
func (PlainCodec) Truncate(f *os.File, size int64) error                { return f.Truncate(size) }
func (PlainCodec) PlaintextSize(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

const (
	ChunkSizePlain  = 4096
	NonceSize       = 12
	TagSize         = 16
	ChunkSizeCipher = ChunkSizePlain + NonceSize + TagSize // 4124
	HeaderSize      = 5
)

type ChunkedAES struct {
	gcm cipher.AEAD
}

// NewChunkedAES initializes block storage with a 32-byte derived key.
func NewChunkedAES(passphrase string) *ChunkedAES {
	key := sha256.Sum256([]byte(passphrase))
	block, _ := aes.NewCipher(key[:])
	gcm, _ := cipher.NewGCM(block)
	return &ChunkedAES{gcm: gcm}
}

func (a *ChunkedAES) readChunk(f *os.File, chunkIdx int64) ([]byte, error) {
	offset := int64(HeaderSize) + (chunkIdx * int64(ChunkSizeCipher))
	cipherBuf := make([]byte, ChunkSizeCipher)

	n, err := f.ReadAt(cipherBuf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n == 0 {
		return nil, io.EOF
	}

	cipherBuf = cipherBuf[:n]

	// Catch OS sparse file holes (all physical zeroes) and resolve to plaintext zeroes
	isZero := true
	for _, b := range cipherBuf {
		if b != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		if n > NonceSize+TagSize {
			return make([]byte, n-NonceSize-TagSize), nil
		}
		return make([]byte, 0), nil
	}

	if len(cipherBuf) < NonceSize+TagSize {
		return nil, errors.New("minionfs: corrupted or truncated cipher chunk")
	}

	nonce, ciphertext := cipherBuf[:NonceSize], cipherBuf[NonceSize:]
	return a.gcm.Open(nil, nonce, ciphertext, nil)
}

func (a *ChunkedAES) writeChunk(f *os.File, chunkIdx int64, plaintext []byte) error {
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}

	ciphertext := a.gcm.Seal(nonce, nonce, plaintext, nil)
	offset := int64(HeaderSize) + (chunkIdx * int64(ChunkSizeCipher))

	_, err := f.WriteAt(ciphertext, offset)
	return err
}

func (a *ChunkedAES) WriteAt(f *os.File, p []byte, off int64) (int, error) {
	stat, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if stat.Size() < int64(HeaderSize) {
		header := make([]byte, HeaderSize)
		copy(header, magicPrefix)
		header[HeaderSize-1] = typeAES
		f.WriteAt(header, 0)
	}

	// If writing past EOF, pad the gap safely so previous AES blocks don't unseal
	currentSize, _ := a.PlaintextSize(f)
	if currentSize < off {
		if err := a.Truncate(f, off); err != nil {
			return 0, err
		}
	}

	written := 0
	for written < len(p) {
		currentOff := off + int64(written)
		chunkIdx := currentOff / int64(ChunkSizePlain)
		chunkOff := currentOff % int64(ChunkSizePlain)

		writeSize := int64(ChunkSizePlain) - chunkOff
		if writeSize > int64(len(p)-written) {
			writeSize = int64(len(p) - written)
		}

		var chunkData []byte
		if chunkOff > 0 || writeSize < int64(ChunkSizePlain) {
			existing, err := a.readChunk(f, chunkIdx)
			if err != nil && err != io.EOF {
				return written, err
			}

			if existing == nil {
				existing = make([]byte, chunkOff+writeSize)
			} else if int64(len(existing)) < chunkOff+writeSize {
				newChunk := make([]byte, chunkOff+writeSize)
				copy(newChunk, existing)
				existing = newChunk
			}

			copy(existing[chunkOff:], p[written:written+int(writeSize)])
			chunkData = existing
		} else {
			chunkData = p[written : written+int(writeSize)]
		}

		if err := a.writeChunk(f, chunkIdx, chunkData); err != nil {
			return written, err
		}
		written += int(writeSize)
	}
	return written, nil
}

func (a *ChunkedAES) ReadAt(f *os.File, p []byte, off int64) (int, error) {
	stat, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if stat.Size() < int64(HeaderSize) {
		return 0, io.EOF
	}

	readTotal := 0
	for readTotal < len(p) {
		currentOff := off + int64(readTotal)
		chunkIdx := currentOff / int64(ChunkSizePlain)
		chunkOff := currentOff % int64(ChunkSizePlain)

		data, err := a.readChunk(f, chunkIdx)
		if err != nil {
			if err == io.EOF {
				break
			}
			return readTotal, err
		}

		if int64(len(data)) <= chunkOff {
			break
		}

		available := int64(len(data)) - chunkOff
		toRead := int64(len(p) - readTotal)
		if available < toRead {
			toRead = available
		}

		copy(p[readTotal:], data[chunkOff:chunkOff+toRead])
		readTotal += int(toRead)

		if int64(len(data)) < int64(ChunkSizePlain) {
			break
		}
	}

	if readTotal == 0 && len(p) > 0 {
		return 0, io.EOF
	}
	return readTotal, nil
}

func (a *ChunkedAES) Truncate(f *os.File, plainSize int64) error {
	if plainSize == 0 {
		return f.Truncate(int64(HeaderSize))
	}

	currentSize, err := a.PlaintextSize(f)
	if err != nil {
		return err
	}

	if plainSize < currentSize {
		chunkIdx := plainSize / int64(ChunkSizePlain)
		chunkOff := plainSize % int64(ChunkSizePlain)
		physicalSize := int64(HeaderSize) + (chunkIdx * int64(ChunkSizeCipher))

		if chunkOff > 0 {
			data, err := a.readChunk(f, chunkIdx)
			if err != nil && err != io.EOF {
				return err
			}
			if data != nil && int64(len(data)) > chunkOff {
				if err := a.writeChunk(f, chunkIdx, data[:chunkOff]); err != nil {
					return err
				}
			}
			physicalSize += int64(chunkOff + NonceSize + TagSize)
		}
		return f.Truncate(physicalSize)
	}

	if plainSize > currentSize {
		if currentSize > 0 {
			// Pre-pad the old EOF chunk so it properly seals into a full chunk before OS expansion
			chunkIdx := currentSize / int64(ChunkSizePlain)
			chunkOff := currentSize % int64(ChunkSizePlain)

			if chunkOff > 0 {
				data, err := a.readChunk(f, chunkIdx)
				if err != nil && err != io.EOF {
					return err
				}

				var newSize int64 = int64(ChunkSizePlain)
				if plainSize/int64(ChunkSizePlain) == chunkIdx {
					newSize = plainSize % int64(ChunkSizePlain)
				}

				expanded := make([]byte, newSize)
				copy(expanded, data)
				if err := a.writeChunk(f, chunkIdx, expanded); err != nil {
					return err
				}
			}
		}

		// Calculate trailing OS truncation bounds
		chunkIdx := plainSize / int64(ChunkSizePlain)
		chunkOff := plainSize % int64(ChunkSizePlain)
		physicalSize := int64(HeaderSize) + (chunkIdx * int64(ChunkSizeCipher))
		if chunkOff > 0 {
			physicalSize += int64(chunkOff + NonceSize + TagSize)
		}
		return f.Truncate(physicalSize)
	}

	return nil
}

func (a *ChunkedAES) PlaintextSize(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}

	s := info.Size()
	if s <= int64(HeaderSize) {
		return 0, nil
	}

	dataSize := s - int64(HeaderSize)
	fullChunks := dataSize / int64(ChunkSizeCipher)
	remainder := dataSize % int64(ChunkSizeCipher)

	plainSize := fullChunks * int64(ChunkSizePlain)
	if remainder > int64(NonceSize+TagSize) {
		plainSize += remainder - int64(NonceSize) - int64(TagSize)
	}
	return plainSize, nil
}

type GzipCodec struct{}

func (c GzipCodec) loadAll(f *os.File) ([]byte, error) {
	f.Seek(0, io.SeekStart)
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return []byte{}, nil
	}
	if len(data) >= magicLen && bytes.Equal(data[:4], magicPrefix) && data[4] == typeGzip {
		r, err := gzip.NewReader(bytes.NewReader(data[magicLen:]))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	return data, nil
}

func (c GzipCodec) saveAll(f *os.File, data []byte) error {
	var buf bytes.Buffer
	buf.Write(magicPrefix)
	buf.WriteByte(typeGzip)
	w := gzip.NewWriter(&buf)
	w.Write(data)
	w.Close()
	f.Truncate(0)
	f.Seek(0, io.SeekStart)
	_, err := f.Write(buf.Bytes())
	return err
}

func (c GzipCodec) ReadAt(f *os.File, p []byte, off int64) (int, error) {
	data, err := c.loadAll(f)
	if err != nil {
		return 0, err
	}
	if off >= int64(len(data)) {
		return 0, io.EOF
	}
	n := copy(p, data[off:])
	return n, nil
}

func (c GzipCodec) WriteAt(f *os.File, p []byte, off int64) (int, error) {
	data, err := c.loadAll(f)
	if err != nil {
		return 0, err
	}
	end := off + int64(len(p))
	if end > int64(len(data)) {
		newData := make([]byte, end)
		copy(newData, data)
		data = newData
	}
	copy(data[off:], p)
	err = c.saveAll(f, data)
	return len(p), err
}

func (c GzipCodec) Truncate(f *os.File, size int64) error {
	data, err := c.loadAll(f)
	if err != nil {
		return err
	}
	if size < int64(len(data)) {
		data = data[:size]
	} else if size > int64(len(data)) {
		newData := make([]byte, size)
		copy(newData, data)
		data = newData
	}
	return c.saveAll(f, data)
}

func (c GzipCodec) PlaintextSize(f *os.File) (int64, error) {
	data, err := c.loadAll(f)
	if err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}
