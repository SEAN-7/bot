package daemon

import (
	"context"
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
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	finished := h.stopAsync()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop after the run finished")
	}
	runs := h.runs(h.mainID)
	if len(runs) != 1 || !ended(runs[0]) || lastEnd(readRecords(runs[0])) != "done" {
		t.Fatalf("active run was interrupted by shutdown: %v", readRecords(runs[0]))
	}
	if n := len(h.requests()); n != 2 {
		t.Fatalf("run did not complete its steps: %d requests", n)
	}
}

func TestDrainStartsNoNewRun(t *testing.T) {
	h := newHarness(t, step("sleep 1", 100), done(), done())
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	h.drop(h.mainHome, "go.md", "go")
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	finished := h.stopAsync()
	h.drop(h.mainHome, "late.md", "arrived during shutdown")
	h.drop(home, "task.md", "arrived during shutdown")
	<-finished
	if names := inboxNames(h.mainHome); len(names) != 1 || names[0] != "late.md" {
		t.Fatalf("message arriving during drain was consumed: %v", names)
	}
	if runs := h.runs(childID("c")); len(runs) != 0 {
		t.Fatalf("a new run started during drain: %v", runs)
	}
}

func TestDrainDeadlineInterrupts(t *testing.T) {
	h := newHarness(t, step("sleep 30", 100))
	h.cfg.DrainTimeout = 300 * time.Millisecond
	h.start()
	h.drop(h.mainHome, "go.md", "go")
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
	if runs := h.runs(h.mainID); len(runs) != 1 || ended(runs[0]) {
		t.Fatal("run interrupted at the deadline must stay unclean so recovery resumes it")
	}
}

func TestDrainingStartsWithTheStopRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{stop: ctx}
	if d.draining() {
		t.Fatal("draining before any stop request")
	}
	cancel()
	// No poll may pass between the request and draining, or an idle agent
	// could start a new run in the gap.
	if !d.draining() {
		t.Fatal("not draining immediately after the stop request")
	}
}
