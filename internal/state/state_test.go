package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScopedAtomicRecoveryAndLock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".azure", "demo"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	record := &Record{SchemaVersion: 1, Scope: "project-a", Service: "simulation", Source: "source", Phase: "verified",
		Outputs: map[string]string{"image": "immutable"}}
	if err := store.Write(record); err != nil {
		t.Fatal(err)
	}
	found, err := store.Read(record.Service, record.Scope)
	if err != nil || found.Outputs["image"] != "immutable" {
		t.Fatalf("record handoff failed: %v %v", found, err)
	}
	if _, err := store.Read(record.Service, "project-b"); err == nil {
		t.Fatal("cross-project recovery accepted")
	}
	if _, err := Open(root, "../escape"); err == nil {
		t.Fatal("environment traversal accepted")
	}
	unlock, err := store.Lock(t.Context(), record.Service)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if _, err := store.Lock(ctx, record.Service); err == nil {
		t.Fatal("concurrent deployment obtained lock")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(record.Service, ".json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(record.Service, record.Scope); err == nil {
		t.Fatal("corrupt state silently ignored")
	}
}
