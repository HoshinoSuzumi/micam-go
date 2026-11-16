package creds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreAtUsesTheGivenDirectory(t *testing.T) {
	dir := t.TempDir()
	store, err := StoreAt(dir)
	if err != nil {
		t.Fatalf("StoreAt: %v", err)
	}
	want := filepath.Join(dir, "credentials.json")
	if store.Path != want {
		t.Fatalf("path = %q, want %q", store.Path, want)
	}
}

func TestStoreAtFallsBackToTheDefault(t *testing.T) {
	store, err := StoreAt("")
	if err != nil {
		t.Fatalf("StoreAt: %v", err)
	}
	if !strings.HasSuffix(store.Path, filepath.Join(".micam", "credentials.json")) {
		t.Fatalf("default path = %q", store.Path)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	store, err := StoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("StoreAt: %v", err)
	}

	c, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Region != DefaultRegion {
		t.Fatalf("region = %q, want %q", c.Region, DefaultRegion)
	}
	if c.HasSession() || c.CanRenew() {
		t.Fatal("an empty store must not report a usable session")
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	store, err := StoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("StoreAt: %v", err)
	}

	want := &Credentials{
		User:      "15023365994",
		UserID:    "1234567890",
		Region:    "cn",
		DeviceID:  "ABCDEFGHIJKLMNOP",
		PassToken: "token",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != *want {
		t.Fatalf("round trip changed the credentials:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "micam")
	store := &Store{Path: filepath.Join(dir, "credentials.json")}

	if err := store.Save(&Credentials{Region: "cn"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(store.Path); err != nil {
		t.Fatalf("credentials were not written: %v", err)
	}
}

func TestDeleteRemovesTheFile(t *testing.T) {
	store, err := StoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("StoreAt: %v", err)
	}
	if err := store.Save(&Credentials{Region: "cn"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Fatalf("the file is still there: %v", err)
	}
	// Deleting a second time must not fail.
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete on a missing file: %v", err)
	}
}
