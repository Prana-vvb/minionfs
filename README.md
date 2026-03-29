# How to use:

## Prerequisites

Install [go](https://go.dev/doc/install) and [just](https://github.com/casey/just#installation) 

## Usage

```
Usage: just <recipe> [arguments...]

Available recipes:
    # Builds the minionfs binary.
    # Args: name
    build               # [alias: b]
    # Cleans up build artifacts and removes the binary
    # Args: name
    clean               # [alias: c]
    default             # Shows this help menu
    # Runs the application on the specified mount point without or with debug logs
    # Args: /path/to/mountpoint [-d]
    run mntpoint dbg="" # [alias: r]
```
