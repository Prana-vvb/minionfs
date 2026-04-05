# MinionFS — Mini UnionFS

A lightweight Union Filesystem built with Go and FUSE (Filesystem in Userspace).  
MinionFS merges a read-only lower layer and a writable upper layer into a single unified directory view, implementing the core semantics of a production overlay filesystem (like OverlayFS).

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [How It Works](#how-it-works)
   - [Layer Merging](#layer-merging)
   - [Copy-on-Write](#copy-on-write)
   - [Whiteouts](#whiteouts)
3. [Usage](#usage)
4. [Building](#building)
5. [Testing](#testing)
6. [File Structure](#file-structure)

---

## Architecture Overview

```
┌─────────────────────────────┐
│        Mounted View         │  ← what the user sees (read/write)
└────────────┬────────────────┘
             │
    ┌────────┴────────┐
    │   MinionFS      │  ← FUSE kernel module ↔ bazil.org/fuse
    └────────┬────────┘
    ┌────────┴────────┐
    │  Layer Resolver │
    └──────┬──────┬───┘
           │      │
    ┌──────┴──┐  ┌┴────────┐
    │  Upper  │  │  Lower  │
    │ (R + W) │  │  (R/O)  │
    └─────────┘  └─────────┘
```

The upper directory is the writable layer. All mutations (creates, writes, deletes) land here.  
The lower directory is the read-only base layer. It is never modified.

---

## How It Works

### Layer Merging

When you read a directory listing or look up a file, MinionFS follows this resolution order:

1. **Check for a whiteout** in the upper layer — if one exists, the entry is treated as deleted and is invisible.
2. **Check the upper layer** — if the file exists here, it is used (upper always shadows lower).
3. **Check the lower layer** — if the file exists here, it is returned read-only from the merged view.
4. **ENOENT** — file not found in either layer.

This means the merged view contains the union of both layers, with upper taking precedence.

### Copy-on-Write

The lower layer is never written to directly. When a user writes to a file that exists only in the lower layer, MinionFS automatically:

1. Copies the file from the lower layer to the upper layer.
2. Applies the write to the upper copy.

All subsequent reads and writes go to the upper copy. The lower original is untouched.

### Whiteouts

Whiteouts allow deleting files from the lower layer without physically modifying it.

When a file is deleted from the merged view:

| Where the file lives | Action taken |
|---|---|
| Upper layer only | File is removed from the upper directory directly. |
| Both layers | Upper copy is removed; a whiteout marker is created to hide the lower copy. |
| Lower layer only | A whiteout marker file is created in the upper directory. |

**Whiteout marker format:**  
A zero-byte file named `.wh.<original_filename>` is created in the upper directory.

**Example:**  
Deleting `hello.txt` (which exists only in the lower layer) creates:
```
upper/.wh.hello.txt
```
The merged view will no longer show `hello.txt`. The lower directory is unchanged.

Whiteout markers are always hidden from the merged directory listing — users never see `.wh.*` files.

---

## Usage

```bash
minionfs [-d] <lowerdir> <upperdir> <mountpoint>
```

| Argument | Description |
|---|---|
| `-d` | Enable debug logging (optional) |
| `<lowerdir>` | Path to the read-only base layer directory |
| `<upperdir>` | Path to the writable upper layer directory |
| `<mountpoint>` | Path where the merged filesystem will be mounted |

**Example:**
```bash
mkdir lower upper mnt

echo "base file" > lower/base.txt

minionfs lower/ upper/ mnt/

ls mnt/              # shows base.txt
echo "edit" >> mnt/base.txt    # triggers Copy-on-Write → upper/base.txt is created
rm mnt/base.txt                # creates upper/.wh.base.txt
```

To unmount:
```bash
fusermount -u mnt/
```

---

## Building

**Prerequisites:** Go 1.21+, FUSE libraries (`libfuse-dev` on Linux / macFUSE on macOS)

```bash
# Using the Justfile
just build

# Or directly
go build cmd/minionfs/main.go
```

---

## Testing
 
### Unit Tests
 
Run the Go unit tests with coverage:
 
```bash
# Run all tests
just test

# Summary percentage
go test ./internal/fs/ -cover
 
# Per-function breakdown
go test ./internal/fs/ -coverprofile=coverage.out && go tool cover -func=coverage.out
 
# Interactive HTML coverage report
go test ./internal/fs/ -coverprofile=coverage.out && go tool cover -html=coverage.out
```
 
 
## File Structure
 
```
minionfs/
├── cmd/
│   └── minionfs/
│       └── main.go          # Entry point, flag parsing, FUSE mount/unmount
├── internal/
│   └── fs/
│       ├── fs.go            # FS + File + Dir struct definitions, inode counter
│       ├── dir.go           # Directory operations: Lookup, ReadDirAll, Mkdir,
│       │                    #   Create, Remove (with whiteout logic)
│       ├── file.go          # File operations: Read, Write (CoW), Flush, Fsync
│       ├── fs_test.go       # Shared test helpers + FS root tests
│       ├── dir_test.go      # Unit tests for directory operations
│       └── file_test.go     # Unit tests for file operations
├── lower/                   # Example lower (base) layer
├── upper/                   # Example upper (writable) layer
├── go.mod
├── go.sum
├── Justfile
└── README.md
```
