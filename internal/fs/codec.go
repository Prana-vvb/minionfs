package fs

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// Magic header: [M][F][S][0x00][type_byte]
var magicPrefix = []byte{0x4D, 0x46, 0x53, 0x00}

const (
	magicLen  = 5
	typePlain = byte(0x00)
	typeAES   = byte(0x01)
	typeGzip  = byte(0x02)
)

// FileCodec transparently encodes/decodes file data at the disk boundary.
// f.data in memory is always plaintext; encoding only happens on disk writes.
type FileCodec interface {
	Encode(plaintext []byte) ([]byte, error)
	Decode(payload []byte) ([]byte, error)
}

// --- PlainCodec: no-op ---

type PlainCodec struct{}

func (PlainCodec) Encode(data []byte) ([]byte, error) { return data, nil }
func (PlainCodec) Decode(data []byte) ([]byte, error) { return data, nil }

// --- AESCodec: AES-256-GCM ---

type AESCodec struct {
	key [32]byte
}

// NewAESCodec derives a 32-byte AES key from the given passphrase via SHA-256.
func NewAESCodec(passphrase string) *AESCodec {
	return &AESCodec{key: sha256.Sum256([]byte(passphrase))}
}

// Encode encrypts plaintext and prepends the MFS magic header.
// Disk layout: [5-byte header][12-byte nonce][ciphertext+16-byte GCM tag]
func (a *AESCodec) Encode(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(a.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	// Seal appends ciphertext+tag to nonce
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return addHeader(ciphertext, typeAES), nil
}

// Decode decrypts payload (without the magic header).
func (a *AESCodec) Decode(payload []byte) ([]byte, error) {
	block, err := aes.NewCipher(a.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(payload) < gcm.NonceSize() {
		return nil, errors.New("minionfs: ciphertext too short")
	}
	nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// --- GzipCodec ---

type GzipCodec struct{}

// Encode compresses plaintext and prepends the MFS magic header.
func (GzipCodec) Encode(plaintext []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return addHeader(buf.Bytes(), typeGzip), nil
}

// Decode decompresses payload (without the magic header).
func (GzipCodec) Decode(payload []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// --- Header helpers ---

func addHeader(data []byte, t byte) []byte {
	header := make([]byte, magicLen)
	copy(header, magicPrefix)
	header[magicLen-1] = t
	return append(header, data...)
}

func hasHeader(data []byte) bool {
	return len(data) >= magicLen && bytes.Equal(data[:len(magicPrefix)], magicPrefix)
}

// DecodeFromDisk detects the magic header and decodes accordingly.
// Files without a header (e.g. lower-layer plaintext) are returned as-is.
func DecodeFromDisk(data []byte, codec FileCodec) ([]byte, error) {
	if codec == nil {
		codec = PlainCodec{}
	}
	if !hasHeader(data) {
		// Legacy / lower-layer plaintext — no decoding needed.
		return data, nil
	}
	typeByte := data[magicLen-1]
	payload := data[magicLen:]

	switch typeByte {
	case typePlain:
		return payload, nil
	case typeAES:
		aesc, ok := codec.(*AESCodec)
		if !ok {
			return nil, fmt.Errorf("minionfs: file is AES-encrypted but no AES key provided")
		}
		return aesc.Decode(payload)
	case typeGzip:
		// Gzip needs no key — always decodable.
		return GzipCodec{}.Decode(payload)
	default:
		return nil, fmt.Errorf("minionfs: unknown codec type 0x%02x", typeByte)
	}
}

// EncodeToDisk encodes plaintext using the given codec.
// PlainCodec is a no-op: data is stored without a header so lower-layer
// files remain human-readable.
func EncodeToDisk(plaintext []byte, codec FileCodec) ([]byte, error) {
	if codec == nil {
		return plaintext, nil
	}
	if _, ok := codec.(PlainCodec); ok {
		return plaintext, nil
	}
	return codec.Encode(plaintext)
}
