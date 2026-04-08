package fs

import (
	"bytes"
	"testing"
)

var testCases = []struct {
	name    string
	payload []byte
}{
	{"empty", []byte{}},
	{"small", []byte("hello from minionfs")},
	{"binary", []byte{0x00, 0xFF, 0xAB, 0xCD, 0x12, 0x34}},
	{"large", bytes.Repeat([]byte("ABCDEFGH"), 1024)},
}

// --- AESCodec ---

func TestAESRoundtrip(t *testing.T) {
	codec := NewAESCodec("supersecret")
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := EncodeToDisk(tc.payload, codec)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := DecodeFromDisk(encoded, codec)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, tc.payload) {
				t.Fatalf("roundtrip mismatch: want %q got %q", tc.payload, got)
			}
		})
	}
}

func TestAESWrongKey(t *testing.T) {
	encoded, err := EncodeToDisk([]byte("secret data"), NewAESCodec("correct-key"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	_, err = DecodeFromDisk(encoded, NewAESCodec("wrong-key"))
	if err == nil {
		t.Fatal("expected decryption to fail with wrong key, got nil")
	}
}

func TestAESNonceIsRandom(t *testing.T) {
	codec := NewAESCodec("key")
	data := []byte("same plaintext")
	enc1, _ := EncodeToDisk(data, codec)
	enc2, _ := EncodeToDisk(data, codec)
	if bytes.Equal(enc1, enc2) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonce not random)")
	}
}

// --- GzipCodec ---

func TestGzipRoundtrip(t *testing.T) {
	codec := GzipCodec{}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := EncodeToDisk(tc.payload, codec)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := DecodeFromDisk(encoded, codec)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, tc.payload) {
				t.Fatalf("roundtrip mismatch: want %q got %q", tc.payload, got)
			}
		})
	}
}

// --- PlainCodec ---

func TestPlainCodecNoHeader(t *testing.T) {
	codec := PlainCodec{}
	data := []byte("plain text, no magic header")
	encoded, err := EncodeToDisk(data, codec)
	if err != nil {
		t.Fatal(err)
	}
	if hasHeader(encoded) {
		t.Fatal("PlainCodec should not add a magic header")
	}
	if !bytes.Equal(encoded, data) {
		t.Fatal("PlainCodec encode should be a no-op")
	}
}

// --- Magic header detection ---

func TestPlainFileNoHeaderDecodesAsIs(t *testing.T) {
	// Simulates a lower-layer file that was never encoded.
	raw := []byte("hello from lower")
	got, err := DecodeFromDisk(raw, NewAESCodec("key"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("plain file should pass through unchanged, got %q", got)
	}
}

func TestGzipDecodesWithoutMatchingCodec(t *testing.T) {
	// Gzip files should always be decodable regardless of the configured codec.
	encoded, _ := EncodeToDisk([]byte("compressed content"), GzipCodec{})
	got, err := DecodeFromDisk(encoded, PlainCodec{})
	if err != nil {
		t.Fatalf("gzip should decode with any codec: %v", err)
	}
	if string(got) != "compressed content" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestAESFileWithNoKeyReturnsError(t *testing.T) {
	encoded, _ := EncodeToDisk([]byte("secret"), NewAESCodec("key"))
	_, err := DecodeFromDisk(encoded, PlainCodec{})
	if err == nil {
		t.Fatal("expected error when decoding AES file without a key")
	}
}

// --- Double-encode safety ---

func TestDoubleEncodeAES(t *testing.T) {
	codec := NewAESCodec("key")
	original := []byte("data")

	enc1, _ := EncodeToDisk(original, codec)

	// Simulates what would happen if we accidentally tried to encode
	// already-encoded bytes. DecodeFromDisk should detect the header and
	// return plaintext, preventing re-encryption of ciphertext.
	decoded, err := DecodeFromDisk(enc1, codec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatalf("expected original after decode, got %q", decoded)
	}
}
