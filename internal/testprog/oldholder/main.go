//go:build !windows

// oldholder stands in for an older incoda holding a lane: it creates
// LANEDIR with its registry.lock and a ticket whose lock it holds (its pid
// in the name and the payload, as every release writes them), starts a
// child `sh -c 'sleep 60 & wait'`, and writes "pid N\nchild C\n" to
// READYFILE. When RELEASEFILE appears it lets go of the ticket but keeps
// running, which no real release can be made to do on cue: kill tests use
// it for "the target released its ticket before the SIGSTOP". It exits
// after 60 seconds whatever happens.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: oldholder LANEDIR KEY READYFILE RELEASEFILE")
		os.Exit(2)
	}
	dir, key, ready, release := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
	}
	reg, err := os.OpenFile(filepath.Join(dir, "registry.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fail(err)
	}
	defer reg.Close()
	now := time.Now()
	name := fmt.Sprintf("%020d-%d.ticket", now.UnixNano(), os.Getpid())
	tf, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fail(err)
	}
	if err := unix.Flock(int(tf.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		fail(err)
	}
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "queue": key, "slots": 1,
		"arrival_nano": now.UnixNano(), "command": []string{"oldholder", key}})
	if _, err := tf.Write(b); err != nil {
		fail(err)
	}
	child := exec.Command("sh", "-c", "sleep 60 & wait")
	if err := child.Start(); err != nil {
		fail(err)
	}
	body := fmt.Sprintf("pid %d\nchild %d\n", os.Getpid(), child.Process.Pid)
	if err := os.WriteFile(ready+".tmp", []byte(body), 0o644); err != nil {
		fail(err)
	}
	if err := os.Rename(ready+".tmp", ready); err != nil {
		fail(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(release); err == nil && tf != nil {
			_ = tf.Close() // the kernel drops the flock with the descriptor
			tf = nil
			_ = os.WriteFile(release+".done", nil, 0o644)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "oldholder:", err)
	os.Exit(1)
}
