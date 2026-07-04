package wal

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
)

// Environment variables used to re-exec this test binary as a "crash child"
// for TestFsyncDurabilitySubprocessCrash. Kept package-private and
// test-only: they only ever matter inside this one test's own process
// tree.
const (
	crashChildEnvFlag = "WAL_CRASH_CHILD"
	crashChildEnvDir  = "WAL_CRASH_DIR"
)

// crashChildPayload is the fixed record payload the child process appends;
// the parent checks for this exact byte sequence after the child is killed.
var crashChildPayload = []byte("durable-before-sigkill")

// TestFsyncDurabilitySubprocessCrash closes the gap flagged in subtask
// 1.3.2's verification (regression.jsonl, run 038-verification):
// TestFsyncBeforeApply proves ordering (Append returns only after apply is
// invoked), but does so entirely within one process, reading back through
// the same process's (page-cache-coherent) view of the file — it cannot by
// itself prove the bytes are actually durable across a real crash.
//
// Rigor level and its limitation (documented explicitly, per this
// subtask's design guidance): Writer is hard-coded to *os.File with no
// injected-file seam, so a counting/intercepting fake file is not available
// without a larger, out-of-scope refactor. This test instead uses the
// standard Go technique for the closest achievable equivalent to a real
// crash without OS-level/VM-level fault injection: it re-execs this same
// test binary as a child process (via exec.Command(os.Args[0], ...)) that
// opens a Writer, Appends one record, and — immediately after Append
// returns control to it — sends itself SIGKILL via syscall.Kill. SIGKILL
// cannot be caught, deferred, or delayed, so nothing the child's process
// could have done (buffering, finalizers, a graceful exit path) runs
// afterward. The PARENT test process then reopens the same WAL directory,
// in a different process, and confirms the record is present and intact.
//
// What this proves: no in-Go or process-local buffering held the record's
// bytes back from actually reaching the file only known to the OS via the
// write(2)/fsync(2) syscalls Writer.Append already issues synchronously —
// if the parent (a genuinely separate process, with no shared in-memory
// state with the child) can read the record back after the child was
// annihilated by SIGKILL with no chance to flush anything on exit, the
// bytes must already have been durably written by the time Append returned
// control to the child, not on some later, unSIGKILL-survivable step.
//
// What this does NOT prove: that the kernel's own page cache was flushed
// all the way to the physical disk platter/controller. Proving durability
// across an actual power loss requires OS-level or VM-level crash/fault
// injection (e.g. cutting power to a VM immediately after a syscall
// returns), which is out of scope for a Go unit test running against a
// real filesystem. That stronger guarantee is not established by this test
// and is called out here explicitly rather than silently assumed.
func TestFsyncDurabilitySubprocessCrash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL-based self-crash simulation is not supported on windows")
	}

	if os.Getenv(crashChildEnvFlag) == "1" {
		runCrashChild()
		// runCrashChild sends SIGKILL to this very process and never
		// returns. Reaching this line would mean the self-kill somehow
		// failed to actually kill the process, which would silently
		// invalidate the whole test's premise.
		t.Fatal("crash child: process was not killed after self-SIGKILL; test premise violated")
		return
	}

	dir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^TestFsyncDurabilitySubprocessCrash$", "-test.v=false")
	cmd.Env = append(os.Environ(),
		crashChildEnvFlag+"=1",
		crashChildEnvDir+"="+dir,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr == nil {
		t.Fatalf("crash child exited cleanly (status 0); want it to have been killed by SIGKILL before it could return normally. stderr:\n%s", stderr.String())
	}
	exitErr, ok := runErr.(*exec.ExitError)
	if !ok {
		t.Fatalf("crash child failed to start/run as expected: %v (stderr:\n%s)", runErr, stderr.String())
	}
	waitStatus, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("could not inspect crash child's exit status (unsupported platform for this assertion): %v", exitErr)
	}
	if !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGKILL {
		t.Fatalf("crash child did not die from SIGKILL as expected: signaled=%v signal=%v (stderr:\n%s)", waitStatus.Signaled(), waitStatus.Signal(), stderr.String())
	}

	// The child is now provably gone, with no opportunity to have flushed
	// anything on a graceful exit path. Reopen the same directory, in THIS
	// (parent) process, and confirm the record it appended survived.
	records, err := ReadSegment(segmentPath(dir, 0))
	if err != nil {
		t.Fatalf("ReadSegment after crash-child was SIGKILLed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("segment after crash-child was SIGKILLed has %d records, want exactly 1", len(records))
	}
	if !bytes.Equal(records[0], crashChildPayload) {
		t.Fatalf("record after crash-child was SIGKILLed = %q, want %q", records[0], crashChildPayload)
	}

	// Also verify via the package's normal resume path (OpenWriter), since
	// that is the code path a real recovering process would actually use.
	w, err := OpenWriter(dir, 4096)
	if err != nil {
		t.Fatalf("OpenWriter after crash-child was SIGKILLed: %v", err)
	}
	defer w.Close()
	wantOffset := int64(recordHeaderSize + len(crashChildPayload))
	if got := w.Offset(); got != wantOffset {
		t.Fatalf("Offset() after resuming post-crash directory = %d, want %d (the single durably-appended record, cleanly resumed)", got, wantOffset)
	}
}

// runCrashChild is the child-process half of TestFsyncDurabilitySubprocessCrash:
// it opens a Writer against the directory named by crashChildEnvDir, appends
// crashChildPayload once, and then — immediately after Append returns
// control to it, proving any fsync Append performs has already completed —
// sends itself SIGKILL. It never returns normally.
func runCrashChild() {
	dir := os.Getenv(crashChildEnvDir)
	if dir == "" {
		fmt.Fprintln(os.Stderr, "crash child: missing WAL_CRASH_DIR")
		os.Exit(2)
	}

	w, err := OpenWriter(dir, 4096)
	if err != nil {
		fmt.Fprintf(os.Stderr, "crash child: OpenWriter: %v\n", err)
		os.Exit(2)
	}

	if _, err := w.Append(crashChildPayload); err != nil {
		fmt.Fprintf(os.Stderr, "crash child: Append: %v\n", err)
		os.Exit(2)
	}

	// Append has returned: per Writer.Append's contract, the record has
	// already been fsynced to disk. Kill this process as brutally and
	// immediately as possible, with no chance for any deferred cleanup,
	// buffering, or graceful-exit logic to run.
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)

	// Unreachable if the kill succeeded (it cannot be caught or ignored).
	// If somehow reached, exit loudly and non-zero rather than silently
	// returning to the test framework as if nothing happened.
	fmt.Fprintln(os.Stderr, "crash child: self-SIGKILL did not terminate the process")
	os.Exit(3)
}
