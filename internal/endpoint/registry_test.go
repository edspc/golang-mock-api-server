package endpoint

import (
	"sync"
	"testing"
)

func TestRegistryCreateAndGet(t *testing.T) {
	reg := NewRegistry(10)
	e, err := reg.Create("", "webhooks")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := reg.Get(e.ID.String())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != e {
		t.Error("Get() returned a different endpoint than Create()")
	}
	if got.Name() != "webhooks" {
		t.Errorf("Name() = %q, want webhooks", got.Name())
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	reg := NewRegistry(10)
	for _, id := range []string{"", "nonsense", "0192f0c8-3f4e-8a1b-9c2d-4e5f60718293"} {
		if _, err := reg.Get(id); err != ErrNotFound {
			t.Errorf("Get(%q) error = %v, want ErrNotFound", id, err)
		}
	}
}

func TestRegistryDelete(t *testing.T) {
	reg := NewRegistry(10)
	e, err := reg.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Delete("", e.ID.String()); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := reg.Get(e.ID.String()); err != ErrNotFound {
		t.Errorf("Get() after Delete() error = %v, want ErrNotFound", err)
	}
	if err := reg.Delete("", e.ID.String()); err != ErrNotFound {
		t.Errorf("second Delete() error = %v, want ErrNotFound", err)
	}
}

func TestRegistryListIsNewestFirst(t *testing.T) {
	reg := NewRegistry(10)
	var ids []string
	for i := 0; i < 5; i++ {
		e, err := reg.Create("", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID.String())
	}

	list := reg.List("")
	if len(list) != 5 {
		t.Fatalf("len(List()) = %d, want 5", len(list))
	}
	for i, e := range list {
		want := ids[len(ids)-1-i]
		if e.ID.String() != want {
			t.Errorf("List()[%d] = %s, want %s (newest first)", i, e.ID, want)
		}
	}
}

func TestRegistryConcurrentCreate(t *testing.T) {
	reg := NewRegistry(10)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := reg.Create("", ""); err != nil {
				t.Errorf("Create() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := reg.Len(); got != 50 {
		t.Errorf("Len() = %d, want 50 distinct endpoints", got)
	}
}

// An endpoint is listed only to the identity that created it, and the
// anonymous owner ("") is an identity like any other.
func TestRegistryScopesByOwner(t *testing.T) {
	reg := NewRegistry(10)

	mine, err := reg.Create("me@edspc.dev", "mine")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := reg.Create("someone@else.com", "theirs")
	if err != nil {
		t.Fatal(err)
	}
	anon, err := reg.Create("", "anonymous")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		owner string
		want  *Endpoint
	}{
		{"me@edspc.dev", mine},
		{"someone@else.com", theirs},
		{"", anon},
	} {
		t.Run(tc.owner, func(t *testing.T) {
			list := reg.List(tc.owner)
			if len(list) != 1 || list[0] != tc.want {
				t.Fatalf("List(%q) returned %d endpoints, want only their own", tc.owner, len(list))
			}
			if got := reg.Count(tc.owner); got != 1 {
				t.Errorf("Count(%q) = %d, want 1", tc.owner, got)
			}
			if _, err := reg.GetFor(tc.owner, tc.want.ID.String()); err != nil {
				t.Errorf("GetFor(%q, own endpoint) = %v, want it found", tc.owner, err)
			}
		})
	}

	// Everything else is not found, and cannot be deleted.
	if _, err := reg.GetFor("me@edspc.dev", theirs.ID.String()); err != ErrNotFound {
		t.Errorf("GetFor across owners = %v, want ErrNotFound", err)
	}
	if err := reg.Delete("me@edspc.dev", theirs.ID.String()); err != ErrNotFound {
		t.Errorf("Delete across owners = %v, want ErrNotFound", err)
	}
	if _, err := reg.Get(theirs.ID.String()); err != nil {
		t.Errorf("Get (the callback path) = %v, want it to ignore ownership", err)
	}

	// Len counts the process, not one account.
	if got := reg.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
}
