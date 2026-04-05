alias b := build
alias r := run
alias c := check
alias t := test

bin_name := "bin/minionfs"

# Shows this help menu
default:
    @echo "Usage: just <recipe> [arguments...]"
    @echo ""
    @just --list

# Build the Go binary in bin
build:
    mkdir -p bin
    go build -o {{bin_name}} ./cmd/minionfs

# Run the filesystem (Usage: just run <folder_name>)
run ldir udir mnt dbg="":
    #!/usr/bin/env bash
    mkdir -p {{ldir}} {{udir}} {{mnt}}

    if [[ -f {{bin_name}} ]]; then
        ./{{bin_name}} {{dbg}} {{ldir}} {{udir}} {{mnt}}
    else
        go run ./cmd/minionfs {{dbg}} {{ldir}} {{udir}} {{mnt}}
    fi

# Clean up bin
clean:
    rm -rf bin
    go clean

# Force unmount if the app crashes
unmount mnt:
    fusermount -u {{mnt}} || umount {{mnt}}

# Format and check Go code logic
check:
    go fmt ./...
    go vet ./...
    go mod tidy

# Summary percentage, Per-function breakdown, Interactive HTML coverage report
test:
    go test ./internal/fs/ -cover
    go test ./internal/fs/ -coverprofile=coverage.out && go tool cover -func=coverage.out
    go test ./internal/fs/ -coverprofile=coverage.out && go tool cover -html=coverage.out
