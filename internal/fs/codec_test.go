package fs

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func tempFile(t *testing.T) *os.File {
	path := filepath.Join(t.TempDir(), "codec_test_file")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	return f
}

func TestPlainCodec(t *testing.T) {
	f := tempFile(t)
	defer f.Close()

	codec := PlainCodec{}
	data := []byte("plain text data")

	n, err := codec.WriteAt(f, data, 0)
	if err != nil || n != len(data) {
		t.Fatalf("WriteAt failed: n=%d, err=%v", n, err)
	}

	buf := make([]byte, len(data))
	n, err = codec.ReadAt(f, buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt failed: err=%v", err)
	}
	if !bytes.Equal(buf[:n], data) {
		t.Fatalf("Data mismatch: got %q, want %q", buf[:n], data)
	}

	size, err := codec.PlaintextSize(f)
	if err != nil || size != int64(len(data)) {
		t.Fatalf("PlaintextSize failed: got %d, want %d", size, len(data))
	}
}

func TestChunkedAES_RoundtripAndBoundaryCrossings(t *testing.T) {
	f := tempFile(t)
	defer f.Close()

	codec := NewChunkedAES("supersecret")

	// 1. Write payload that forcefully spans across multiple 4KB chunks
	dataSize := 10000
	data := make([]byte, dataSize)
	rand.Read(data)

	n, err := codec.WriteAt(f, data, 0)
	if err != nil || n != len(data) {
		t.Fatalf("WriteAt failed: n=%d, err=%v", n, err)
	}

	// 2. Verify PlaintextSize calculation ignores GCM tags
	size, err := codec.PlaintextSize(f)
	if err != nil || size != int64(len(data)) {
		t.Fatalf("PlaintextSize failed: got %d, want %d", size, len(data))
	}

	// 3. Read back full data
	buf := make([]byte, dataSize)
	n, err = codec.ReadAt(f, buf, 0)
	if (err != nil && err != io.EOF) || n != len(data) {
		t.Fatalf("ReadAt full failed: n=%d, err=%v", n, err)
	}
	if !bytes.Equal(buf, data) {
		t.Fatal("Read data does not match written data")
	}

	// 4. Read starting from an offset (spans from end of Chunk 0 into Chunk 1)
	offBuf := make([]byte, 200)
	n, err = codec.ReadAt(f, offBuf, 4000)
	if err != nil || n != 200 {
		t.Fatalf("ReadAt offset failed: n=%d, err=%v", n, err)
	}
	if !bytes.Equal(offBuf, data[4000:4200]) {
		t.Fatal("Offset read data mismatch")
	}

	// 5. Overwrite middle of chunk (Triggers the Read-Modify-Write logic)
	newData := []byte("OVERWRITE_DATA")
	_, err = codec.WriteAt(f, newData, 1000)
	if err != nil {
		t.Fatalf("WriteAt offset failed: %v", err)
	}

	checkBuf := make([]byte, len(newData))
	codec.ReadAt(f, checkBuf, 1000)
	if !bytes.Equal(checkBuf, newData) {
		t.Fatal("Overwrite data mismatch")
	}
}

func TestChunkedAES_Truncate(t *testing.T) {
	f := tempFile(t)
	defer f.Close()

	codec := NewChunkedAES("secret")
	data := make([]byte, 8000)
	rand.Read(data)

	codec.WriteAt(f, data, 0)

	// Truncate to 5000 bytes (Cuts cleanly halfway through the second chunk)
	err := codec.Truncate(f, 5000)
	if err != nil {
		t.Fatalf("Truncate failed: %v", err)
	}

	size, _ := codec.PlaintextSize(f)
	if size != 5000 {
		t.Fatalf("PlaintextSize after truncate got %d, want 5000", size)
	}

	buf := make([]byte, 5000)
	n, err := codec.ReadAt(f, buf, 0)
	if n != 5000 {
		t.Fatalf("Expected to read 5000 bytes, got %d", n)
	}
	if !bytes.Equal(buf, data[:5000]) {
		t.Fatal("Truncated data does not match original")
	}

	// Ensure reading past the new EOF returns io.EOF
	eofBuf := make([]byte, 10)
	n, err = codec.ReadAt(f, eofBuf, 5000)
	if n != 0 || err != io.EOF {
		t.Fatalf("Expected EOF reading past truncated size, got n=%d, err=%v", n, err)
	}
}

func TestChunkedAES_WrongKey(t *testing.T) {
	f := tempFile(t)
	defer f.Close()

	codec1 := NewChunkedAES("key1")
	codec1.WriteAt(f, []byte("super secret data"), 0)

	// Attempt to read with a different key
	codec2 := NewChunkedAES("key2")
	buf := make([]byte, 17)
	_, err := codec2.ReadAt(f, buf, 0)

	if err == nil {
		t.Fatal("Expected decryption to fail with wrong key, but it succeeded")
	}
}

func TestGzipCodec_Roundtrip(t *testing.T) {
	f := tempFile(t)
	defer f.Close()

	codec := GzipCodec{}
	data := bytes.Repeat([]byte("compressible_data_"), 100)

	n, err := codec.WriteAt(f, data, 0)
	if err != nil || n != len(data) {
		t.Fatalf("WriteAt failed: %v", err)
	}

	buf := make([]byte, len(data))
	n, err = codec.ReadAt(f, buf, 0)
	if (err != nil && err != io.EOF) || n != len(data) {
		t.Fatalf("ReadAt failed: %v", err)
	}

	if !bytes.Equal(buf, data) {
		t.Fatal("Data mismatch after gzip decompression")
	}

	size, _ := codec.PlaintextSize(f)
	if size != int64(len(data)) {
		t.Fatalf("Expected plain size %d, got %d", len(data), size)
	}
}
