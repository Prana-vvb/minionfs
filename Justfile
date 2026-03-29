alias b := build
alias r := run
alias c := clean

name := "minionfs"
# Shows this help menu
default:
    @echo "Usage: just <recipe> [arguments...]"
    @echo ""
    @just --list

[doc("""Builds the minionfs binary.
Args: name""")]
build:
    go build -o {{name}} cmd/minionfs/main.go

[doc("""Runs the application on the specified mount point without or with debug logs
Args: /path/to/mountpoint [-d]""")]
run mntpoint dbg="":
    #!/usr/bin/env bash
    if [[ -f {{name}} ]]; then
        ./{{name}} {{dbg}} {{mntpoint}}
    else
        go run cmd/minionfs/main.go {{dbg}} {{mntpoint}}
    fi

[doc("""Cleans up build artifacts and removes the binary
Args: name""")]
clean:
    go clean
    rm ./{{name}}
