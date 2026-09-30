package gitx_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The tests need fake git and gh binaries that hang, stay silent or leave a
// grandchild behind. A POSIX shell script cannot do that on Windows, so the
// fake is the test binary itself: fakeBinary links (or copies) it under the
// wanted name and writes the behaviour next to it in "<path>.mode". TestMain
// checks for that file first thing and, when it exists, plays the fake instead
// of running tests. The mode travels in a file, not in argv or the
// environment, because gitx builds both of those itself.

// fakeModeSuffix names the sidecar file that turns a linked test binary into a
// fake. Nothing else creates it, so it also tells the real test run apart.
const fakeModeSuffix = ".mode"

// fakeStopSuffix names the file whose existence ends a lingering grandchild.
const fakeStopSuffix = ".stop"

// fakeModeEnv overrides the sidecar file; the fake uses it to start a
// grandchild that only sleeps.
const fakeModeEnv = "BROOOM_GITX_FAKE_MODE"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		os.Exit(runFake(mode))
	}
	if mode, err := os.ReadFile(os.Args[0] + fakeModeSuffix); err == nil {
		os.Exit(runFake(strings.TrimSpace(string(mode))))
	}
	os.Exit(m.Run())
}

// fakeModes maps a mode to its behaviour; the result is the exit status.
var fakeModes = map[string]func() int{
	"sleep":               func() int { time.Sleep(30 * time.Second); return 0 },
	"sleep-then-done":     fakeSleepThenDone,
	"sleep-until-stopped": fakeSleepUntilStopped,
	"silent-producer":     fakeSilentProducer,
	"hang-grandchild":     fakeHangGrandchild,
	"gh-env":              fakeGHEnv,
}

// runFake plays the behaviour named by mode and returns the exit status.
func runFake(mode string) int {
	if f, ok := fakeModes[mode]; ok {
		return f()
	}
	return 0
}

func fakeSleepThenDone() int {
	time.Sleep(time.Second)
	_, _ = io.WriteString(os.Stdout, "done\n")
	return 0
}

// fakeSilentProducer is invoked as `git -C dir <cmd> ...`: the consumer
// (patch-id) exits at once, the producer (cat-file) never writes and never
// exits.
func fakeSilentProducer() int {
	if len(os.Args) > 3 && os.Args[3] == "cat-file" {
		time.Sleep(30 * time.Second)
	}
	return 0
}

// fakeHangGrandchild leaves a grandchild that inherits stdout and stderr and
// outlives the deadline, so waiting for the pipes would block as long as it
// lives.
func fakeHangGrandchild() int {
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), fakeModeEnv+"=sleep-until-stopped")
	// Do not inherit the working directory: on Windows a live process pins its
	// cwd and the test's temporary directory could not be removed.
	child.Dir = os.TempDir()
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		return 2
	}
	time.Sleep(30 * time.Second)
	return 0
}

// fakeSleepUntilStopped sleeps like the plain "sleep" mode but returns as soon
// as the stop file next to the executable appears, so the test cleanup can end
// the grandchild and free the files (on Windows a running executable and its
// directory cannot be deleted).
func fakeSleepUntilStopped() int {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(os.Args[0] + fakeStopSuffix); err == nil {
			return 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

// fakeGHEnv answers like gh and reveals whether the caller's GIT_* variables
// leaked into its environment.
func fakeGHEnv() int {
	name := "clean"
	if os.Getenv("GIT_DIR") != "" || os.Getenv("GIT_INDEX_FILE") != "" {
		name = "leaked"
	}
	_, _ = io.WriteString(os.Stdout, `[{"headRefName":"`+name+`"}]`+"\n")
	return 0
}

// fakeBinary installs a fake executable named name in a fresh directory and
// returns its path. mode selects the behaviour runFake plays.
func fakeBinary(t *testing.T, name, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot locate the test binary: " + err.Error())
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := linkOrCopy(self, path); err != nil {
		t.Skip("cannot install the fake binary: " + err.Error())
	}
	if err := os.WriteFile(path+fakeModeSuffix, []byte(mode), 0o644); err != nil {
		t.Fatal(err)
	}
	if mode == "hang-grandchild" {
		// Runs before the TempDir removal (cleanups are LIFO): tell the
		// grandchild to exit and give it a moment to release the directory.
		t.Cleanup(func() {
			_ = os.WriteFile(path+fakeStopSuffix, nil, 0o644)
			time.Sleep(500 * time.Millisecond)
		})
	}
	return path
}

// linkOrCopy makes dst run the same program as src. Links are preferred so a
// test does not write a copy of the (large) test binary each time.
func linkOrCopy(src, dst string) error {
	if os.Link(src, dst) == nil {
		return nil
	}
	if runtime.GOOS != "windows" && os.Symlink(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
