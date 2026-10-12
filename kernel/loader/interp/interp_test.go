package interp

import (
	"context"
	"testing"
	"time"

	"github.com/witchsnk/millipede/kernel/loader"
)

func TestInterpLoaderLifecycle(t *testing.T) {
	ldr := NewInterpLoader()
	if ldr.Kind() != loader.BackendInterp {
		t.Fatalf("expected backend %v, got %v", loader.BackendInterp, ldr.Kind())
	}

	ctx := context.Background()
	desc := loader.Descriptor{ID: "test-plugin"}

	inst, err := ldr.Load(ctx, desc)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if inst.ID() != "test-plugin" {
		t.Errorf("expected ID test-plugin, got %s", inst.ID())
	}

	if inst.State() != loader.StateReady {
		t.Errorf("expected StateReady, got %v", inst.State())
	}

	info, err := ldr.Inspect(ctx, "test-plugin")
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if info.State != loader.StateReady {
		t.Errorf("expected StateReady, got %v", info.State)
	}

	if err := ldr.Unload(ctx, "test-plugin"); err != nil {
		t.Fatalf("Unload failed: %v", err)
	}

	if _, ok := ldr.GetInstance("test-plugin"); ok {
		t.Errorf("expected instance removed after Unload")
	}
}

func TestInterpLoaderOptions(t *testing.T) {
	ldr := NewInterpLoaderWithOpts(InterpLoaderOpts{
		Timeout:         10 * time.Second,
		AllowAllImports: true,
	})

	if err := ldr.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig failed: %v", err)
	}
}
