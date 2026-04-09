#!/bin/bash

just build
FUSE_BINARY="./bin/minionfs"
TEST_DIR="./unionfs_test_env"
LOWER_DIR="$TEST_DIR/lower"
UPPER_DIR="$TEST_DIR/upper"
MOUNT_DIR="$TEST_DIR/mnt"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'
echo "Starting Mini-UnionFS Test Suite..."

# Setup
rm -rf "$TEST_DIR"
mkdir -p "$LOWER_DIR" "$UPPER_DIR" "$MOUNT_DIR"
echo "base_only_content" > "$LOWER_DIR/base.txt"
echo "to_be_deleted" > "$LOWER_DIR/delete_me.txt"
$FUSE_BINARY "$LOWER_DIR" "$UPPER_DIR" "$MOUNT_DIR" &
sleep 1

# Test 1: Visibility
echo -n "Test 1: Layer Visibility... "
if grep -q "base_only_content" "$MOUNT_DIR/base.txt"; then echo -e "${GREEN}PASSED${NC}"; else echo -e "${RED}FAILED${NC}"; fi

# Test 2: Copy-on-Write
echo -n "Test 2: Copy-on-Write... "
echo "modified_content" >> "$MOUNT_DIR/base.txt" 2>/dev/null
if [ $(grep -c "modified_content" "$MOUNT_DIR/base.txt" 2>/dev/null) -eq 1 ] && [ $(grep -c "modified_content" "$UPPER_DIR/base.txt" 2>/dev/null) -eq 1 ] && [ $(grep -c "modified_content" "$LOWER_DIR/base.txt" 2>/dev/null) -eq 0 ]; then
    echo -e "${GREEN}PASSED${NC}"
else
    echo -e "${RED}FAILED${NC}"
fi

# Test 3: Whiteout
echo -n "Test 3: Whiteout mechanism... "
rm "$MOUNT_DIR/delete_me.txt" 2>/dev/null
if [ ! -f "$MOUNT_DIR/delete_me.txt" ] && [ -f "$LOWER_DIR/delete_me.txt" ] && [ -f "$UPPER_DIR/.wh.delete_me.txt" ]; then
    echo -e "${GREEN}PASSED${NC}"
else
    echo -e "${RED}FAILED${NC}"
fi

# Teardown
fusermount -u "$MOUNT_DIR" 2>/dev/null || umount "$MOUNT_DIR" 2>/dev/null
rm -rf "$TEST_DIR"
just clean
echo "Test Suite Completed."
