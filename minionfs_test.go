package minionfs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type testEnv struct {
	lower string
	upper string
	mnt   string
	bin   string
}

var env *testEnv

func TestMain(m *testing.M) {
	lower, _ := os.MkdirTemp("", "minionfs-lower-*")
	upper, _ := os.MkdirTemp("", "minionfs-upper-*")
	mnt, _ := os.MkdirTemp("", "minionfs-mnt-*")
	binDir, _ := os.MkdirTemp("", "minionfs-bin-*")
	bin := filepath.Join(binDir, "minionfs")
	defer os.RemoveAll(binDir)

	out, err := exec.Command("go", "build", "-o", bin, "./cmd/minionfs/main.go").CombinedOutput()
	if err != nil {
		panic("build failed: " + string(out))
	}

	env = &testEnv{lower: lower, upper: upper, mnt: mnt, bin: bin}

	code := m.Run()

	exec.Command("fusermount", "-u", "-z", mnt).Run()
	os.RemoveAll(lower)
	os.RemoveAll(upper)
	os.RemoveAll(mnt)
	os.Exit(code)
}

func mountEnv(t *testing.T) {
	t.Helper()

	// Unmount any leftover mount from a previous test
	exec.Command("fusermount", "-u", env.mnt).Run()
	time.Sleep(50 * time.Millisecond)

	// Fresh layers
	os.RemoveAll(env.lower)
	os.RemoveAll(env.upper)
	os.MkdirAll(env.lower, 0755)
	os.MkdirAll(env.upper, 0755)

	writeFile(t, filepath.Join(env.lower, "lower_only.txt"), "lower only file\n")
	writeFile(t, filepath.Join(env.lower, "shared.txt"), "lower shared\n")
	os.MkdirAll(filepath.Join(env.lower, "subdir"), 0755)
	writeFile(t, filepath.Join(env.lower, "subdir", "nested.txt"), "nested file\n")
	writeFile(t, filepath.Join(env.upper, "upper_only.txt"), "upper only file\n")
	writeFile(t, filepath.Join(env.upper, "shared.txt"), "upper shared\n")

	cmd := exec.Command(env.bin, env.lower, env.upper, env.mnt)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("mount start: %v", err)
	}

	// Poll until the mount is live
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(filepath.Join(env.mnt, "lower_only.txt")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Cleanup(func() {
		// Signal the process to shut down cleanly, then wait for it to exit.
		// This is much faster than relying solely on fusermount to trigger the
		// signal, because cmd.Wait() would otherwise block until the process
		// handles the interrupt itself.
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGINT)
		}
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// If it hasn't exited, force-unmount and kill
			exec.Command("fusermount", "-u", "-z", env.mnt).Run()
			cmd.Process.Kill()
			<-done
		}
	})
}

// ── Layer Merging ─────────────────────────────────────────────────────────────

func TestLowerLayerFileVisible(t *testing.T) {
	mountEnv(t)
	assertExists(t, filepath.Join(env.mnt, "lower_only.txt"))
}

func TestUpperLayerFileVisible(t *testing.T) {
	mountEnv(t)
	assertExists(t, filepath.Join(env.mnt, "upper_only.txt"))
}

func TestUpperShadowsLower(t *testing.T) {
	mountEnv(t)
	assertContains(t, filepath.Join(env.mnt, "shared.txt"), "upper shared")
}

func TestSubdirAndNestedFileVisible(t *testing.T) {
	mountEnv(t)
	assertExists(t, filepath.Join(env.mnt, "subdir", "nested.txt"))
}

// ── Write Operations ──────────────────────────────────────────────────────────

func TestCreateNewFile(t *testing.T) {
	mountEnv(t)
	writeFile(t, filepath.Join(env.mnt, "new_file.txt"), "new content\n")
	assertExists(t, filepath.Join(env.upper, "new_file.txt"))
	assertContains(t, filepath.Join(env.upper, "new_file.txt"), "new content")
}

func TestCopyOnWrite(t *testing.T) {
	mountEnv(t)

	assertMissing(t, filepath.Join(env.upper, "lower_only.txt"))

	f, err := os.OpenFile(filepath.Join(env.mnt, "lower_only.txt"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	f.WriteString("cow modified\n")
	f.Close()

	assertExists(t, filepath.Join(env.upper, "lower_only.txt"))
	assertContains(t, filepath.Join(env.upper, "lower_only.txt"), "cow modified")
}

// ── Delete / Whiteout ─────────────────────────────────────────────────────────

func TestDeleteUpperOnlyFile(t *testing.T) {
	mountEnv(t)

	if err := os.Remove(filepath.Join(env.mnt, "upper_only.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	assertMissing(t, filepath.Join(env.mnt, "upper_only.txt"))
	assertMissing(t, filepath.Join(env.upper, "upper_only.txt"))
	assertMissing(t, filepath.Join(env.upper, ".wh.upper_only.txt"))
}

func TestDeleteFileInBothLayers(t *testing.T) {
	mountEnv(t)

	f, err := os.OpenFile(filepath.Join(env.mnt, "lower_only.txt"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open for CoW: %v", err)
	}
	f.WriteString("cow\n")
	f.Close()
	assertExists(t, filepath.Join(env.upper, "lower_only.txt"))

	if err := os.Remove(filepath.Join(env.mnt, "lower_only.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	assertMissing(t, filepath.Join(env.mnt, "lower_only.txt"))
	assertMissing(t, filepath.Join(env.upper, "lower_only.txt"))
	assertExists(t, filepath.Join(env.upper, ".wh.lower_only.txt"))
}

func TestDeleteLowerOnlyFileCreatesWhiteout(t *testing.T) {
	mountEnv(t)

	writeFile(t, filepath.Join(env.lower, "whiteout_test.txt"), "fresh lower\n")
	pollExists(t, filepath.Join(env.mnt, "whiteout_test.txt"))

	if err := os.Remove(filepath.Join(env.mnt, "whiteout_test.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	assertMissing(t, filepath.Join(env.mnt, "whiteout_test.txt"))
	assertExists(t, filepath.Join(env.upper, ".wh.whiteout_test.txt"))
}

func TestWhiteoutPersistsDuringMount(t *testing.T) {
	mountEnv(t)

	writeFile(t, filepath.Join(env.lower, "persist_test.txt"), "data\n")
	pollExists(t, filepath.Join(env.mnt, "persist_test.txt"))

	if err := os.Remove(filepath.Join(env.mnt, "persist_test.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	assertMissing(t, filepath.Join(env.mnt, "persist_test.txt"))
	time.Sleep(100 * time.Millisecond)
	assertMissing(t, filepath.Join(env.mnt, "persist_test.txt"))
}

// ── Directory Operations ───────────────────────────────────────────────────────

func TestCreateDirectory(t *testing.T) {
	mountEnv(t)

	if err := os.Mkdir(filepath.Join(env.mnt, "new_dir"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	assertExists(t, filepath.Join(env.upper, "new_dir"))
	assertExists(t, filepath.Join(env.mnt, "new_dir"))
}

func TestRemoveDirectory(t *testing.T) {
	mountEnv(t)

	if err := os.Mkdir(filepath.Join(env.mnt, "new_dir"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Remove(filepath.Join(env.mnt, "new_dir")); err != nil {
		t.Fatalf("rmdir: %v", err)
	}

	assertMissing(t, filepath.Join(env.upper, "new_dir"))
	assertMissing(t, filepath.Join(env.mnt, "new_dir"))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func pollExists(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to appear", path)
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("expected %s to not exist, but it does", path)
	}
}

func assertContains(t *testing.T, path, substr string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), substr) {
		t.Errorf("expected %s to contain %q, got: %q", path, substr, string(data))
	}
}
