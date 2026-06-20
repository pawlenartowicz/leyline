package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/internal/server/allowed"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

// allowAll creates an allowed.Rules that permits everything (all extensions,
// no size limit) for sync. Uses a real temp file since allowed.Load requires one.
func allowAll(t *testing.T) *allowed.Rules {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "allowed")
	if err := os.WriteFile(p, []byte("[sync]\n*.md\n*.txt\n*.png\n"), 0644); err != nil {
		t.Fatalf("write allowed file: %v", err)
	}
	r, err := allowed.Load(p)
	if err != nil {
		t.Fatalf("load allowed: %v", err)
	}
	return r
}

// allowMDOnly creates an allowed.Rules that only allows .md files.
func allowMDOnly(t *testing.T) *allowed.Rules {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "allowed")
	if err := os.WriteFile(p, []byte("[sync]\n*.md\n"), 0644); err != nil {
		t.Fatalf("write allowed file: %v", err)
	}
	r, err := allowed.Load(p)
	if err != nil {
		t.Fatalf("load allowed: %v", err)
	}
	return r
}

// commitFile writes path with data to the vault and creates a commit. Returns
// the commit hash as a protocol.Hash.
func commitFile(t *testing.T, g *storage.GitStore, vaultDir, path string, data []byte) protocol.Hash {
	t.Helper()
	fullPath := filepath.Join(vaultDir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	ops := []protocol.Op{{
		Seq:  1,
		Type: protocol.OpWrite,
		Path: path,
		Data: data,
		TS:   time.Now().UnixMilli(),
	}}
	h, err := g.CommitOps(ops, "tester")
	if err != nil {
		t.Fatalf("CommitOps for %s: %v", path, err)
	}
	return h
}

// deleteFile stages a deletion commit. Returns the commit hash.
func deleteFile(t *testing.T, g *storage.GitStore, vaultDir, path string) protocol.Hash {
	t.Helper()
	fullPath := filepath.Join(vaultDir, filepath.FromSlash(path))
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", path, err)
	}
	ops := []protocol.Op{{
		Seq:  1,
		Type: protocol.OpDelete,
		Path: path,
		TS:   time.Now().UnixMilli(),
	}}
	h, err := g.CommitOps(ops, "tester")
	if err != nil {
		t.Fatalf("CommitOps delete %s: %v", path, err)
	}
	return h
}

// allowMDYAML permits *.md and *.yaml — used to prove web.yaml (which matches
// the extension whitelist) is still withheld from non-admins by the
// vaultconfig scoping, not merely by the extensionless-name accident.
func allowMDYAML(t *testing.T) *allowed.Rules {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "allowed")
	if err := os.WriteFile(p, []byte("[sync]\n*.md\n*.yaml\n"), 0644); err != nil {
		t.Fatalf("write allowed file: %v", err)
	}
	r, err := allowed.Load(p)
	if err != nil {
		t.Fatalf("load allowed: %v", err)
	}
	return r
}

// TestServerManifestDigestAtHead_RecipientAware verifies the digest set
// matches what the per-recipient send layer delivers: admins see the
// .leyline/vaultconfig/* tree, non-admins do not. Without this the
// up_to_date drift check would mismatch forever for one of the two roles.
func TestServerManifestDigestAtHead_RecipientAware(t *testing.T) {
	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	commitFile(t, g, dir, "a.md", []byte("alpha"))
	commitFile(t, g, dir, ".leyline/vaultconfig/access", []byte("ley_key role=admin"))
	// web.yaml matches *.yaml — proves vaultconfig scoping, not the
	// extensionless-name accident, is what hides it from readers.
	head := commitFile(t, g, dir, ".leyline/vaultconfig/web.yaml", []byte("title: x"))

	allow := allowMDYAML(t)

	adminDigest, err := ServerManifestDigestAtHead(g, allow, head, true)
	if err != nil {
		t.Fatalf("admin digest: %v", err)
	}
	readerDigest, err := ServerManifestDigestAtHead(g, allow, head, false)
	if err != nil {
		t.Fatalf("reader digest: %v", err)
	}

	if adminDigest == readerDigest {
		t.Fatal("admin and reader digests must differ when vaultconfig files exist")
	}

	// The reader's view is exactly the non-vaultconfig content (a.md). Both
	// access (extensionless) and web.yaml (matches *.yaml) are withheld.
	wantReader := protocol.ManifestDigest([]protocol.ManifestEntry{
		{Path: "a.md", Hash: protocol.HashBytes([]byte("alpha"))},
	})
	if readerDigest != wantReader {
		t.Errorf("reader digest must exclude all vaultconfig files:\n got=%x\nwant=%x", readerDigest, wantReader)
	}
}

func TestWalkCatchup_SimpleSequence(t *testing.T) {
	// commit A: write x.md=v1
	// commit B: write y.md=v1
	// commit C: write x.md=v2
	// WalkCatchup(A..C) → [write(y.md, v1), write(x.md, v2)]

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	hashA := commitFile(t, g, dir, "x.md", []byte("a"))
	_ = commitFile(t, g, dir, "y.md", []byte("a"))
	hashC := commitFile(t, g, dir, "x.md", []byte("b"))

	allow := allowAll(t)
	result, err := WalkCatchup(g, allow, hashA, hashC)
	if err != nil {
		t.Fatalf("WalkCatchup: %v", err)
	}

	// Build index by path.
	byPath := make(map[string]protocol.Op)
	for _, op := range result.Ops {
		byPath[op.Path] = op
	}

	if len(result.Ops) != 2 {
		t.Fatalf("expected 2 ops, got %d: %v", len(result.Ops), result.Ops)
	}

	if op, ok := byPath["x.md"]; !ok {
		t.Error("missing write for x.md")
	} else {
		if op.Type != protocol.OpWrite {
			t.Errorf("x.md: expected write, got %q", op.Type)
		}
		if string(op.Data) != "b" {
			t.Errorf("x.md: expected data v2, got %q", op.Data)
		}
		if op.PreHash != nil {
			t.Error("x.md: PreHash should be nil on derived op")
		}
	}

	if op, ok := byPath["y.md"]; !ok {
		t.Error("missing write for y.md")
	} else {
		if op.Type != protocol.OpWrite {
			t.Errorf("y.md: expected write, got %q", op.Type)
		}
		if string(op.Data) != "a" {
			t.Errorf("y.md: expected data v1, got %q", op.Data)
		}
		if op.PreHash != nil {
			t.Error("y.md: PreHash should be nil on derived op")
		}
	}
}

func TestWalkCatchup_Deletes(t *testing.T) {
	// commit A: write x.md
	// commit B: delete x.md
	// WalkCatchup(A..B) → [delete(x.md)]

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	hashA := commitFile(t, g, dir, "x.md", []byte("hello"))
	hashB := deleteFile(t, g, dir, "x.md")

	allow := allowAll(t)
	result, err := WalkCatchup(g, allow, hashA, hashB)
	if err != nil {
		t.Fatalf("WalkCatchup: %v", err)
	}

	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d: %v", len(result.Ops), result.Ops)
	}
	op := result.Ops[0]
	if op.Type != protocol.OpDelete {
		t.Errorf("expected delete, got %q", op.Type)
	}
	if op.Path != "x.md" {
		t.Errorf("expected path x.md, got %q", op.Path)
	}
	if op.PreHash != nil {
		t.Error("PreHash should be nil on derived op")
	}
}

func TestWalkCatchup_RenameAcrossCommits(t *testing.T) {
	// commit A (base parent): write x.md=v1
	// commit B (base, excluded): already has x.md
	// commit C: rename x.md → y.md (implemented as delete + write at new path)
	// WalkCatchup(B..C) → [write(y.md, v1)] — normalized

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	// base setup: write x.md
	hashBase := commitFile(t, g, dir, "x.md", []byte("a"))

	// "rename" x.md → y.md: delete x.md, write y.md with same content.
	content := []byte("a")
	// Remove old file and write new file, then commit as two ops.
	if err := os.Remove(filepath.Join(dir, "x.md")); err != nil {
		t.Fatalf("remove x.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "y.md"), content, 0644); err != nil {
		t.Fatalf("write y.md: %v", err)
	}
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpDelete, Path: "x.md", TS: time.Now().UnixMilli()},
		{Seq: 2, Type: protocol.OpWrite, Path: "y.md", Data: content, TS: time.Now().UnixMilli()},
	}
	hashHead, err := g.CommitOps(ops, "tester")
	if err != nil {
		t.Fatalf("CommitOps rename: %v", err)
	}

	allow := allowAll(t)
	result, err := WalkCatchup(g, allow, hashBase, hashHead)
	if err != nil {
		t.Fatalf("WalkCatchup: %v", err)
	}

	// Normalize: x.md is deleted, y.md is written. No intermediate state
	// is emitted (no write(x.md, v1) followed by delete(x.md)).
	byPath := make(map[string]protocol.Op)
	for _, op := range result.Ops {
		byPath[op.Path] = op
	}

	if _, hasX := byPath["x.md"]; !hasX {
		// x.md delete should appear since it was deleted in this range.
		t.Error("expected delete(x.md) in ops")
	} else if byPath["x.md"].Type != protocol.OpDelete {
		t.Errorf("x.md: expected delete, got %q", byPath["x.md"].Type)
	}

	if _, hasY := byPath["y.md"]; !hasY {
		t.Error("expected write(y.md) in ops")
	} else if byPath["y.md"].Type != protocol.OpWrite {
		t.Errorf("y.md: expected write, got %q", byPath["y.md"].Type)
	}

	// No extra ops beyond x.md and y.md.
	if len(result.Ops) != 2 {
		t.Errorf("expected 2 ops, got %d: %v", len(result.Ops), result.Ops)
	}

	// All PreHash must be nil.
	for _, op := range result.Ops {
		if op.PreHash != nil {
			t.Errorf("op %s %s: PreHash should be nil", op.Type, op.Path)
		}
	}
}

func TestWalkCatchup_FilteredByAllowed(t *testing.T) {
	// commit A: write x.md and x.png
	// commit B: write y.md and y.png
	// WalkCatchup(A..B) with allowMDOnly → only [write(y.md)]

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	// Commit A: write x.md and x.png
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("a"), 0644); err != nil {
		t.Fatalf("write x.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.png"), []byte("img"), 0644); err != nil {
		t.Fatalf("write x.png: %v", err)
	}
	opsA := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("a"), TS: time.Now().UnixMilli()},
		{Seq: 2, Type: protocol.OpWrite, Path: "x.png", Data: []byte("img"), TS: time.Now().UnixMilli()},
	}
	hashA, err := g.CommitOps(opsA, "tester")
	if err != nil {
		t.Fatalf("CommitOps A: %v", err)
	}

	// Commit B: write y.md and y.png
	if err := os.WriteFile(filepath.Join(dir, "y.md"), []byte("b"), 0644); err != nil {
		t.Fatalf("write y.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "y.png"), []byte("img2"), 0644); err != nil {
		t.Fatalf("write y.png: %v", err)
	}
	opsB := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "y.md", Data: []byte("b"), TS: time.Now().UnixMilli()},
		{Seq: 2, Type: protocol.OpWrite, Path: "y.png", Data: []byte("img2"), TS: time.Now().UnixMilli()},
	}
	hashB, err := g.CommitOps(opsB, "tester")
	if err != nil {
		t.Fatalf("CommitOps B: %v", err)
	}

	allow := allowMDOnly(t)
	result, err := WalkCatchup(g, allow, hashA, hashB)
	if err != nil {
		t.Fatalf("WalkCatchup: %v", err)
	}

	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op (only y.md), got %d: %v", len(result.Ops), result.Ops)
	}
	op := result.Ops[0]
	if op.Path != "y.md" {
		t.Errorf("expected y.md, got %q", op.Path)
	}
	if op.Type != protocol.OpWrite {
		t.Errorf("expected write, got %q", op.Type)
	}
}

func TestWalkBootstrap_StreamsAllAllowedFiles(t *testing.T) {
	// Setup: write x.md, y.md, z.txt at HEAD.
	// WalkBootstrap → yields ops for all three.

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write x.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "y.md"), []byte("world"), 0644); err != nil {
		t.Fatalf("write y.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "z.txt"), []byte("text"), 0644); err != nil {
		t.Fatalf("write z.txt: %v", err)
	}
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("hello"), TS: time.Now().UnixMilli()},
		{Seq: 2, Type: protocol.OpWrite, Path: "y.md", Data: []byte("world"), TS: time.Now().UnixMilli()},
		{Seq: 3, Type: protocol.OpWrite, Path: "z.txt", Data: []byte("text"), TS: time.Now().UnixMilli()},
	}
	head, err := g.CommitOps(ops, "tester")
	if err != nil {
		t.Fatalf("CommitOps: %v", err)
	}

	allow := allowAll(t)
	var got []protocol.Op
	if err := WalkBootstrap(g, allow, head, func(op protocol.Op) bool {
		got = append(got, op)
		return true
	}); err != nil {
		t.Fatalf("WalkBootstrap: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 ops, got %d: %v", len(got), got)
	}

	byPath := make(map[string]protocol.Op)
	for _, op := range got {
		byPath[op.Path] = op
	}
	for _, path := range []string{"x.md", "y.md", "z.txt"} {
		op, ok := byPath[path]
		if !ok {
			t.Errorf("missing op for %s", path)
			continue
		}
		if op.Type != protocol.OpWrite {
			t.Errorf("%s: expected write, got %q", path, op.Type)
		}
		if op.PreHash != nil {
			t.Errorf("%s: PreHash should be nil on bootstrap op", path)
		}
	}
}

func TestWalkBootstrap_RespectsAllowedFilter(t *testing.T) {
	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("md"), 0644); err != nil {
		t.Fatalf("write x.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.png"), []byte("png"), 0644); err != nil {
		t.Fatalf("write x.png: %v", err)
	}
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("md"), TS: time.Now().UnixMilli()},
		{Seq: 2, Type: protocol.OpWrite, Path: "x.png", Data: []byte("png"), TS: time.Now().UnixMilli()},
	}
	head, err := g.CommitOps(ops, "tester")
	if err != nil {
		t.Fatalf("CommitOps: %v", err)
	}

	allow := allowMDOnly(t)
	var got []protocol.Op
	if err := WalkBootstrap(g, allow, head, func(op protocol.Op) bool {
		got = append(got, op)
		return true
	}); err != nil {
		t.Fatalf("WalkBootstrap: %v", err)
	}
	if len(got) != 1 || got[0].Path != "x.md" {
		t.Errorf("expected only x.md, got %+v", got)
	}
}

// TestWalkCatchup_CarriesCommitAuthor verifies that ops emitted by
// WalkCatchup carry the originating commit's git author Name in Op.Author.
// commitFile uses "tester" as the author; the test commits two files under
// distinct authors and checks the per-op Author.
func TestWalkCatchup_CarriesCommitAuthor(t *testing.T) {
	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	// Base commit by author "alice".
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0644); err != nil {
		t.Fatalf("write a.md: %v", err)
	}
	opsA := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "a.md", Data: []byte("a"), TS: time.Now().UnixMilli()},
	}
	hashA, err := g.CommitOps(opsA, "alice")
	if err != nil {
		t.Fatalf("CommitOps alice: %v", err)
	}

	// Second commit by author "bob": writes b.md.
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("b"), 0644); err != nil {
		t.Fatalf("write b.md: %v", err)
	}
	opsB := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "b.md", Data: []byte("b"), TS: time.Now().UnixMilli()},
	}
	hashB, err := g.CommitOps(opsB, "bob")
	if err != nil {
		t.Fatalf("CommitOps bob: %v", err)
	}

	// WalkCatchup(A..B) — only one commit in range, by bob.
	allow := allowAll(t)
	result, err := WalkCatchup(g, allow, hashA, hashB)
	if err != nil {
		t.Fatalf("WalkCatchup: %v", err)
	}
	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d: %+v", len(result.Ops), result.Ops)
	}
	if result.Ops[0].Author != "bob" {
		t.Errorf("write Author: got %q, want %q", result.Ops[0].Author, "bob")
	}

	// Now delete b.md as "carol" — the catchup op should carry "carol".
	if err := os.Remove(filepath.Join(dir, "b.md")); err != nil {
		t.Fatalf("remove b.md: %v", err)
	}
	opsC := []protocol.Op{
		{Seq: 1, Type: protocol.OpDelete, Path: "b.md", TS: time.Now().UnixMilli()},
	}
	hashC, err := g.CommitOps(opsC, "carol")
	if err != nil {
		t.Fatalf("CommitOps carol: %v", err)
	}
	resultDel, err := WalkCatchup(g, allow, hashB, hashC)
	if err != nil {
		t.Fatalf("WalkCatchup delete: %v", err)
	}
	if len(resultDel.Ops) != 1 {
		t.Fatalf("expected 1 delete op, got %d: %+v", len(resultDel.Ops), resultDel.Ops)
	}
	if resultDel.Ops[0].Type != protocol.OpDelete {
		t.Errorf("expected delete, got %q", resultDel.Ops[0].Type)
	}
	if resultDel.Ops[0].Author != "carol" {
		t.Errorf("delete Author: got %q, want %q", resultDel.Ops[0].Author, "carol")
	}
}

// TestWalkBootstrap_LeavesAuthorEmpty verifies bootstrap ops have empty
// Author — per-file authorship at HEAD would require walking history per
// file, so bootstrap deliberately omits it.
func TestWalkBootstrap_LeavesAuthorEmpty(t *testing.T) {
	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("v"), 0644); err != nil {
		t.Fatalf("write x.md: %v", err)
	}
	ops := []protocol.Op{
		{Seq: 1, Type: protocol.OpWrite, Path: "x.md", Data: []byte("v"), TS: time.Now().UnixMilli()},
	}
	head, err := g.CommitOps(ops, "alice")
	if err != nil {
		t.Fatalf("CommitOps: %v", err)
	}

	allow := allowAll(t)
	var got []protocol.Op
	if err := WalkBootstrap(g, allow, head, func(op protocol.Op) bool {
		got = append(got, op)
		return true
	}); err != nil {
		t.Fatalf("WalkBootstrap: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 op, got %d", len(got))
	}
	if got[0].Author != "" {
		t.Errorf("Author: got %q, want empty", got[0].Author)
	}
}

func TestWalkBootstrap_YieldStopsIteration(t *testing.T) {
	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}
	// Seed at least 5 files.
	var ops []protocol.Op
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("file-%d.md", i)
		data := []byte(name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		ops = append(ops, protocol.Op{
			Seq: uint64(i + 1), Type: protocol.OpWrite,
			Path: name, Data: data, TS: time.Now().UnixMilli(),
		})
	}
	head, err := g.CommitOps(ops, "tester")
	if err != nil {
		t.Fatalf("CommitOps: %v", err)
	}

	allow := allowAll(t)
	count := 0
	if err := WalkBootstrap(g, allow, head, func(op protocol.Op) bool {
		count++
		return count < 2 // stop after 2 yields
	}); err != nil {
		t.Fatalf("WalkBootstrap: %v", err)
	}
	if count != 2 {
		t.Errorf("walker yielded %d times, want 2 (stop signal ignored)", count)
	}
}
