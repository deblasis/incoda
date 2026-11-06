//go:build windows

package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestStreamJobFileDeliversPostSleepTail drives the wiring, not the helper:
// a real process writes the tail and dies, and the stream must deliver
// every byte of it. The loop's order (death check before drain) is what
// makes the post-death drain carry the tail: bytes that land in the sleep
// window before the writer's exit are fetched only by the drain after
// death is observed. A death branch that reads one buffer instead of
// draining loses the tail's end, and this test goes red for exactly that.
//
// The writer is a plain cmd that pauses (so a poll cycle observes the head
// while the tail is still unwritten), then appends the tail and exits.
func TestStreamJobFileDeliversPostSleepTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.out")
	tailPath := filepath.Join(dir, "tail.bin")

	head := []byte("head-line\r\n")
	if err := os.WriteFile(path, head, 0o644); err != nil {
		t.Fatal(err)
	}
	tail := bytes.Repeat([]byte("T"), 512*1024)
	if err := os.WriteFile(tailPath, tail, 0o644); err != nil {
		t.Fatal(err)
	}

	writer := exec.Command("cmd", "/c",
		"ping -n 2 127.0.0.1 >nul & type tail.bin >> job.out")
	writer.Dir = dir
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- streamJobFile(context.Background(), f, writer.Process.Pid, &out)
	}()

	werr := writer.Wait()
	if werr != nil {
		t.Fatal(werr)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("streamJobFile did not return within 10s of the writer dying")
	}
	want := append(append([]byte{}, head...), tail...)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("delivered %d bytes of %d: the tail after the last poll must arrive whole, not a buffer's worth",
			out.Len(), len(want))
	}
}
