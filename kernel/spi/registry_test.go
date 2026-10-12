package spi

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestRegisterGetList(t *testing.T) {
	r := NewRegistry[string]()
	if err := r.Register("a", "va"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("b", "vb"); err != nil {
		t.Fatal(err)
	}
	if v, ok := r.Get("a"); !ok || v != "va" {
		t.Fatalf("Get a = %v,%v", v, ok)
	}
	if got := r.List(); len(got) != 2 || got[0] != "va" || got[1] != "vb" {
		t.Fatalf("List = %v", got)
	}
}

func TestDuplicate(t *testing.T) {
	r := NewRegistry[int]()
	_ = r.Register("x", 1)
	err := r.Register("x", 2)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestConcurrentRead(t *testing.T) {
	r := NewRegistry[int]()
	for i := 0; i < 100; i++ {
		_ = r.Register(fmt.Sprintf("k%d", i), i)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.List()
				_, _ = r.Get("k10")
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()
}

func TestReplaceCOW(t *testing.T) {
	r := NewRegistry[int]()
	_ = r.Register("old", 1)
	r.Replace(map[string]int{"new": 2}, []string{"new"})
	if _, ok := r.Get("old"); ok {
		t.Fatal("old should be gone after Replace")
	}
	if v, ok := r.Get("new"); !ok || v != 2 {
		t.Fatalf("new = %v,%v", v, ok)
	}
}
