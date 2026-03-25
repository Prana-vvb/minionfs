alias b := build
alias r := run

default:
    just --list

build name="minionfs":
    go build -o {{name}} cmd/minionfs/main.go

run mntpoint dbg="":
    go run cmd/minionfs/main.go {{dbg}} {{mntpoint}}
