package endpoint

import (
	"sync"
	"testing"
)

func TestRegistryCreateAndGet(t *testing.T) {
	reg := NewRegistry(10)
	e, err := reg.Create("webhooks")
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
	if got.Name != "webhooks" {
		t.Errorf("Name = %q, want webhooks", got.Name)
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
	e, err := reg.Create("")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Delete(e.ID.String()); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := reg.Get(e.ID.String()); err != ErrNotFound {
		t.Errorf("Get() after Delete() error = %v, want ErrNotFound", err)
	}
	if err := reg.Delete(e.ID.String()); err != ErrNotFound {
		t.Errorf("second Delete() error = %v, want ErrNotFound", err)
	}
}

func TestRegistryListIsNewestFirst(t *testing.T) {
	reg := NewRegistry(10)
	var ids []string
	for i := 0; i < 5; i++ {
		e, err := reg.Create("")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID.String())
	}

	list := reg.List()
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
			if _, err := reg.Create(""); err != nil {
				t.Errorf("Create() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := reg.Len(); got != 50 {
		t.Errorf("Len() = %d, want 50 distinct endpoints", got)
	}
}
