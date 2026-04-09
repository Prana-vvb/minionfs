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
run ldir udir mnt *flags:
    #!/usr/bin/env bash
    set -e

    HAS_ENC=0
    HAS_COMP=0

    for arg in {{flags}}; do
        if [[ "$arg" == "--encrypt-key" ]]; then
            HAS_ENC=1
        elif [[ "$arg" == "--compress" ]]; then
            HAS_COMP=1
        fi
    done

    if [[ $HAS_ENC -eq 1 && $HAS_COMP -eq 1 ]]; then
        echo "Error: '--encrypt' and '--compress' are mutually exclusive." >&2
        exit 1
    fi

    mkdir -p {{ldir}} {{udir}} {{mnt}}

    if [[ -f {{bin_name}} ]]; then
        ./{{bin_name}} {{flags}} {{ldir}} {{udir}} {{mnt}}
    else
        go run ./cmd/minionfs {{flags}} {{ldir}} {{udir}} {{mnt}}
    fi

# Clean up bin
clean:
    rm -rf bin *.out
    go clean

# Force unmount if the app crashes
unmount mnt:
    fusermount -u {{mnt}} || umount {{mnt}}

# Format and check Go code logic
check:
    go fmt ./...
    go vet ./...
    go mod tidy

# Go tests and given project test script
test:
    go test ./internal/fs/ -v -coverprofile=coverage.out && go tool cover -html=coverage.out
    ./test_unionfs.sh
