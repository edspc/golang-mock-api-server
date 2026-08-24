package mock

import (
	"strconv"
	"sync"
	"testing"
)

func TestRecorderEvictsOldest(t *testing.T) {
	rec := NewRecorder(3)
	for i := 0; i < 5; i++ {
		rec.Record(Entry{Path: "/" + strconv.Itoa(i)})
	}
	got := rec.Entries()
	if len(got) != 3 {
		t.Fatalf("len(Entries()) = %d, want 3", len(got))
	}
	if got[0].Path != "/2" || got[2].Path != "/4" {
		t.Errorf("Entries() = %v, want the last three requests oldest-first", got)
	}
}

func TestRecorderReset(t *testing.T) {
	rec := NewRecorder(0)
	rec.Record(Entry{Path: "/a"})
	rec.Reset()
	if got := rec.Entries(); len(got) != 0 {
		t.Errorf("Entries() after Reset() = %v, want empty", got)
	}
}

func TestRecorderEntriesIsACopy(t *testing.T) {
	rec := NewRecorder(2)
	rec.Record(Entry{Path: "/a"})
	got := rec.Entries()
	got[0].Path = "/mutated"
	if rec.Entries()[0].Path != "/a" {
		t.Error("mutating the Entries() result changed the recorder's state")
	}
}

func TestRecorderConcurrent(t *testing.T) {
	rec := NewRecorder(16)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); rec.Record(Entry{Path: "/x"}) }()
		go func() { defer wg.Done(); _ = rec.Entries() }()
	}
	wg.Wait()
	if got := len(rec.Entries()); got != 16 {
		t.Errorf("len(Entries()) = %d, want the capacity 16", got)
	}
}
