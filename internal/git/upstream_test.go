package git

import (
	"testing"
)

func TestGit_UpstreamRemote(t *testing.T) {
	tmp := t.TempDir()
	g := NewGit(tmp)

	// Need an actual git repo initialized
	runGit(t, tmp, "init", "--initial-branch", "main")

	// Initially, no upstream remote
	has, err := g.HasUpstreamRemote()
	if err != nil {
		t.Fatalf("HasUpstreamRemote initially: %v", err)
	}
	if has {
		t.Fatal("expected no upstream remote initially")
	}

	url, err := g.GetUpstreamURL()
	if err != nil {
		t.Fatalf("GetUpstreamURL initially: %v", err)
	}
	if url != "" {
		t.Errorf("expected empty upstream URL initially, got %q", url)
	}

	upstream1 := "https://example.com/upstream1.git"
	if err := g.AddUpstreamRemote(upstream1); err != nil {
		t.Fatalf("AddUpstreamRemote %q: %v", upstream1, err)
	}

	has, err = g.HasUpstreamRemote()
	if err != nil {
		t.Fatalf("HasUpstreamRemote after add: %v", err)
	}
	if !has {
		t.Fatal("expected upstream remote to exist")
	}

	url, err = g.GetUpstreamURL()
	if err != nil {
		t.Fatalf("GetUpstreamURL after add: %v", err)
	}
	if url != upstream1 {
		t.Errorf("expected upstream URL %q, got %q", upstream1, url)
	}

	// Idempotent add (same URL)
	if err := g.AddUpstreamRemote(upstream1); err != nil {
		t.Fatalf("AddUpstreamRemote idempotent %q: %v", upstream1, err)
	}

	// Update (different URL)
	upstream2 := "https://example.com/upstream2.git"
	if err := g.AddUpstreamRemote(upstream2); err != nil {
		t.Fatalf("AddUpstreamRemote update %q: %v", upstream2, err)
	}

	url, err = g.GetUpstreamURL()
	if err != nil {
		t.Fatalf("GetUpstreamURL after update: %v", err)
	}
	if url != upstream2 {
		t.Errorf("expected upstream URL %q, got %q", upstream2, url)
	}
}
