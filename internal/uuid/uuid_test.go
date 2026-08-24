package uuid

import (
	"regexp"
	"sync"
	"testing"
	"time"
)

var canonical = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewV8Format(t *testing.T) {
	u, err := NewV8()
	if err != nil {
		t.Fatalf("NewV8() error = %v", err)
	}
	if got := u.String(); !canonical.MatchString(got) {
		t.Errorf("String() = %q, want a canonical v8 UUID with the RFC 9562 variant", got)
	}
	if !u.IsV8() {
		t.Error("IsV8() = false, want true")
	}
}

func TestNewV8Unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		u, err := NewV8()
		if err != nil {
			t.Fatalf("NewV8() error = %v", err)
		}
		if seen[u.String()] {
			t.Fatalf("NewV8() returned a duplicate: %s", u)
		}
		seen[u.String()] = true
	}
}

func TestV8IsTimeOrdered(t *testing.T) {
	var g generator
	early, err := g.newAt(time.UnixMilli(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	late, err := g.newAt(time.UnixMilli(2_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if early.String() >= late.String() {
		t.Errorf("%s >= %s, want earlier timestamps to sort first", early, late)
	}
	if got := early.Time(); !got.Equal(time.UnixMilli(1_000_000).UTC()) {
		t.Errorf("Time() = %v, want the encoded timestamp", got)
	}
}

func TestV8OrdersWithinOneMillisecond(t *testing.T) {
	var g generator
	at := time.UnixMilli(1_700_000_000_000)

	prev := ""
	for i := 0; i < 100; i++ {
		u, err := g.newAt(at)
		if err != nil {
			t.Fatal(err)
		}
		if got := u.String(); got <= prev {
			t.Fatalf("id %d = %s, want it to sort after %s", i, got, prev)
		} else {
			prev = got
		}
	}
}

func TestV8SequenceOverflowAdvancesTime(t *testing.T) {
	var g generator
	at := time.UnixMilli(1_700_000_000_000)

	prev := ""
	for i := 0; i <= maxSeq+2; i++ {
		u, err := g.newAt(at)
		if err != nil {
			t.Fatal(err)
		}
		if got := u.String(); got <= prev {
			t.Fatalf("id %d = %s, want ordering to survive counter overflow (prev %s)", i, got, prev)
		} else {
			prev = got
		}
	}
}

func TestV8ClockGoingBackwardsStaysOrdered(t *testing.T) {
	var g generator
	late, err := g.newAt(time.UnixMilli(2_000_000))
	if err != nil {
		t.Fatal(err)
	}
	rewound, err := g.newAt(time.UnixMilli(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if rewound.String() <= late.String() {
		t.Errorf("%s <= %s, want a rewound clock to keep issuing increasing IDs", rewound, late)
	}
}

func TestNewV8ConcurrentIsMonotonicAndUnique(t *testing.T) {
	const n = 500
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := NewV8()
			if err != nil {
				t.Errorf("NewV8() error = %v", err)
				return
			}
			ids <- u.String()
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool, n)
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id under concurrency: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Errorf("generated %d unique ids, want %d", len(seen), n)
	}
}

func TestParseRoundTrip(t *testing.T) {
	u, err := NewV8()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(u.String())
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", u, err)
	}
	if got != u {
		t.Errorf("Parse() = %s, want %s", got, u)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, s := range []string{
		"",
		"not-a-uuid",
		"0192f0c8-3f4e-8a1b-9c2d-4e5f60718293x",
		"0192f0c83f4e8a1b9c2d4e5f60718293",
		"0192f0c8-3f4e-8a1b-9c2d-4e5f6071829g",
	} {
		t.Run(s, func(t *testing.T) {
			if _, err := Parse(s); err == nil {
				t.Errorf("Parse(%q) error = nil, want ErrInvalid", s)
			}
		})
	}
}

func TestIsV8RejectsOtherVersions(t *testing.T) {
	// A v4 UUID: correct variant, wrong version.
	u, err := Parse("0192f0c8-3f4e-4a1b-9c2d-4e5f60718293")
	if err != nil {
		t.Fatal(err)
	}
	if u.IsV8() {
		t.Error("IsV8() = true for a v4 UUID, want false")
	}
}
