package handlers

import (
	"encoding/json"
	"errors"
	"proxmox-manager/internal/balance"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProv: n1 and n2 have 100 GB (92 GB assignable after the 8 GB reserve); VM 50 (20 GB) runs on n2, VM 51 (10 GB) on n1.
func stoppedVM(f *fakeProv, id int, node string, gb int) {
	f.vms[id] = &fvm{node: node, name: "vm" + strconv.Itoa(id), status: "stopped", cfg: map[string]string{"memory": strconv.Itoa(gb * 1024), "cores": "2"}}
}

func startOK(t *testing.T, f *fakeProv, node string, id int, now time.Time) (*startRefusal, bool) {
	t.Helper()
	called := false
	_, ref, err := startGuarded(f, 40, node, "qemu", id, now, func() (json.RawMessage, error) {
		called = true
		return json.RawMessage(`"UPID"`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ref, called
}

func resetStarts(t *testing.T) {
	t.Helper()
	startedRecently.Lock()
	startedRecently.m = map[string]time.Time{}
	startedRecently.Unlock()
}

func TestStartRefusedWhenTheNodeHasNoRoomAndNothingIsStarted(t *testing.T) {
	resetStarts(t)
	f := newFakeProv()
	stoppedVM(f, 70, "n2", 80) // n2: 20 GB running + 80 = 100 > 92
	ref, called := startOK(t, f, "n2", 70, time.Now())
	if ref == nil || called {
		t.Fatalf("started without room: %+v called=%v", ref, called)
	}
	if ref.MissingBytes() != 8*(int64(1)<<30) || ref.Node != "n2" || ref.VMID != 70 {
		t.Fatalf("%+v", ref)
	}
	msg := ref.Message()
	for _, want := range []string{"RAM insufficiente su n2", "richiede 80 GB", "ne mancano 8", "assegnabile 92 GB", "accese 20 GB"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("%q misses %q", msg, want)
		}
	}
}

func TestStartAllowedWhenItFitsAndStoppedGuestsDoNotCount(t *testing.T) {
	resetStarts(t)
	f := newFakeProv()
	stoppedVM(f, 70, "n1", 40)
	stoppedVM(f, 71, "n1", 90) // stopped: uses no RAM, so it must not block the start of 70
	ref, called := startOK(t, f, "n1", 70, time.Now())
	if ref != nil || !called {
		t.Fatalf("%+v %v", ref, called)
	}
}

func TestStartLeavesAlonewhatNeedsNoCheck(t *testing.T) {
	resetStarts(t)
	f := newFakeProv()
	// already running, a template, an unknown guest and a guest of another node: no check, Proxmox answers
	for name, c := range map[string]struct {
		node string
		id   int
	}{"running": {"n1", 51}, "template": {"n1", 9000}, "unknown": {"n1", 12345}, "wrong node": {"n2", 51}} {
		ref, called := startOK(t, f, c.node, c.id, time.Now())
		if ref != nil || !called {
			t.Errorf("%s: %+v %v", name, ref, called)
		}
	}
}

func TestStartCountsAGuestStartedAMomentAgoThatProxmoxStillShowsStopped(t *testing.T) {
	resetStarts(t)
	f := newFakeProv()
	stoppedVM(f, 70, "n2", 50)
	stoppedVM(f, 71, "n2", 50)
	now := time.Now()
	if ref, called := startOK(t, f, "n2", 70, now); ref != nil || !called { // 20 + 50 = 70 <= 92
		t.Fatalf("first: %+v %v", ref, called)
	}
	// 70 is still "stopped" for Proxmox (the fake never changes it): 20 + 50 + 50 > 92
	if ref, called := startOK(t, f, "n2", 71, now.Add(10*time.Second)); ref == nil || called {
		t.Fatalf("second passed on RAM that the first one is already taking: %+v %v", ref, called)
	}
	// after the reservation expired the shown state is what counts
	if ref, called := startOK(t, f, "n2", 71, now.Add(startReserveTTL+time.Second)); ref != nil || !called {
		t.Fatalf("after the TTL: %+v %v", ref, called)
	}
}

func TestStartFailureDoesNotReserveRAM(t *testing.T) {
	resetStarts(t)
	f := newFakeProv()
	stoppedVM(f, 70, "n2", 50)
	stoppedVM(f, 71, "n2", 50)
	_, ref, err := startGuarded(f, 40, "n2", "qemu", 70, time.Now(), func() (json.RawMessage, error) { return nil, errors.New("proxmox 500") })
	if err == nil || ref != nil {
		t.Fatalf("%v %+v", err, ref)
	}
	if ref, called := startOK(t, f, "n2", 71, time.Now()); ref != nil || !called {
		t.Fatalf("a failed start reserved RAM: %+v %v", ref, called)
	}
}

// failing: the cluster cannot be read
type failingStartAPI struct{ *fakeProv }

func (failingStartAPI) GetClusterResources(string) (json.RawMessage, error) {
	return nil, errors.New("proxmox 500")
}

func TestStartGoesOnWhenTheClusterCannotBeRead(t *testing.T) {
	resetStarts(t)
	called := false
	_, ref, err := startGuarded(failingStartAPI{newFakeProv()}, 40, "n2", "qemu", 70, time.Now(), func() (json.RawMessage, error) {
		called = true
		return nil, nil
	})
	if err != nil || ref != nil || !called {
		t.Fatalf("%v %+v %v", err, ref, called)
	}
}

// Parallel starts on one node with room for two 30 GB guests: exactly two start, whatever the interleaving.
func TestStartParallelRequestsNeverOverbookTheNode(t *testing.T) {
	for round := 0; round < 10; round++ {
		resetStarts(t)
		f := newFakeProv()
		for i := 0; i < 6; i++ {
			stoppedVM(f, 70+i, "n2", 30) // n2 has 92 - 20 = 72 GB free: two fit, a third does not
		}
		var started int32
		var wg sync.WaitGroup
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				if ref, called := startOK(t, f, "n2", id, time.Now()); ref == nil && called {
					atomic.AddInt32(&started, 1)
				}
			}(70 + i)
		}
		wg.Wait()
		if started != 2 {
			t.Fatalf("round %d: %d guests started, 2 fit", round, started)
		}
	}
}

func TestStartRefusedForTooFewThreads(t *testing.T) {
	resetStarts(t)
	f := newFakeProv()
	stoppedVM(f, 70, "n2", 4)
	f.vms[70].cfg["cores"] = "64" // the fake nodes have 16 threads
	ref, called := startOK(t, f, "n2", 70, time.Now())
	if ref == nil || called || !strings.Contains(ref.Message(), "non ha abbastanza thread") || !strings.Contains(ref.Message(), "ne servono 64, ne ha 16") {
		t.Fatalf("%+v %v", ref, called)
	}
}

func TestStartRefusalMessages(t *testing.T) {
	off := &startRefusal{Node: "n9", VMID: 1, Name: "x", Reasons: []balance.PlaceReason{{Code: balance.PlaceOffline}}}
	if !strings.Contains(off.Message(), "n9 e' fuori linea") || off.MissingBytes() != 0 {
		t.Fatal(off.Message())
	}
	if (&startRefusal{}).Message() == "" {
		t.Fatal("empty message")
	}
}
