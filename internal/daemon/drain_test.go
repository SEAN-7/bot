package daemon

import (
	"testing"
	"time"
)

// stopAsync requests shutdown and returns a channel closed when Run returns.
func (h *harness) stopAsync() chan struct{} {
	finished := make(chan struct{})
	done, cancel := h.done, h.cancel
	h.cancel = nil
	cancel()
	go func() {
		if err := <-done; err != nil {
			h.t.Errorf("Run: %v", err)
		}
		close(finished)
	}()
	return finished
}

func TestDrainFinishesActiveRun(t *testing.T) {
	h := newHarness(t, step("sleep 1", 100), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	finished := h.stopAsync()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop after the run finished")
	}
	runs := h.runs(h.cfg.Home)
	if len(runs) != 1 || !ended(runs[0]) || lastEnd(readRecords(runs[0])) != "done" {
		t.Fatalf("active run was interrupted by shutdown: %v", readRecords(runs[0]))
	}
	if n := len(h.requests()); n != 2 {
		t.Fatalf("run did not complete its steps: %d requests", n)
	}
}

func TestDrainStartsNoNewRun(t *testing.T) {
	h := newHarness(t, step("sleep 1", 100), done(), done())
	child := makeChild(t, h, "c", true)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	finished := h.stopAsync()
	h.drop(h.cfg.Home, "late.md", "arrived during shutdown")
	h.drop(child, "task.md", "arrived during shutdown")
	<-finished
	if names := inboxNames(h.cfg.Home); len(names) != 1 || names[0] != "late.md" {
		t.Fatalf("message arriving during drain was consumed: %v", names)
	}
	if runs := h.runs(child); len(runs) != 0 {
		t.Fatalf("a new run started during drain: %v", runs)
	}
}

func TestDrainDeadlineInterrupts(t *testing.T) {
	h := newHarness(t, step("sleep 30", 100))
	h.cfg.DrainTimeout = 300 * time.Millisecond
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	begin := time.Now()
	finished := h.stopAsync()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("drain deadline not enforced")
	}
	if time.Since(begin) > 3*time.Second {
		t.Fatalf("stop took %s", time.Since(begin))
	}
	if runs := h.runs(h.cfg.Home); len(runs) != 1 || ended(runs[0]) {
		t.Fatal("run interrupted at the deadline must stay unclean so recovery resumes it")
	}
}
