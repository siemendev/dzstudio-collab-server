package fsprojects

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/siemendev/dzstudio-collab-server/core"
)

func TestVersionsOutsideEditsAndReplacedRevisions(t *testing.T) {
	now := int64(1_000_000)
	s := New("fs", t.TempDir(), "chernarus", func() int64 { return now })
	if _, ok, _ := s.CurrentRevision("mod-a"); ok {
		t.Fatal("no project yet")
	}
	r1, err := s.Save(core.SaveRequest{Key: "mod-a", Files: []core.ProjectFile{{Name: "manifest", Content: `{"v":1}`}, {Name: "f-north", Content: "Aä"}}, Deleted: nil, Meta: core.ProjectMeta{ProjectName: "North", Map: "livonia"}})
	if err != nil || r1.Revision != "r1" {
		t.Fatalf("first save: %v %v", r1, err)
	}
	head, _ := s.Head("mod-a", "")
	if head.ProjectName != "North" || head.Map != "livonia" || len(head.Files) != 2 {
		t.Fatalf("head: %+v", head)
	}
	r2, _ := s.Save(core.SaveRequest{Key: "mod-a", Files: []core.ProjectFile{{Name: "manifest", Content: `{"v":2}`}, {Name: "f-north", Content: "Aä"}}, Deleted: nil, Meta: core.ProjectMeta{}})
	if r2.Files[0].Revision != "2" || r2.Files[1].Revision != "1" {
		t.Fatalf("per-file revisions: %+v", r2.Files)
	}
	if c, _ := s.ReadFile("mod-a", "r1", "manifest"); c != `{"v":1}` {
		t.Fatalf("the replaced revision stays readable: %q", c)
	}

	if err := s.EditOutside("mod-a", map[string]string{"f-north": "edited"}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile("mod-a", "r2", "f-north"); !errors.Is(err, core.ErrRevisionUnknown) {
		t.Fatalf("a file changed in place no longer belongs to its revision: %v", err)
	}
	rev, _, _ := s.CurrentRevision("mod-a")
	if rev != "r3" {
		t.Fatalf("an edit from outside is taken over as a new revision: %s", rev)
	}
	if changed, _ := s.ChangedSince("mod-a", "r2"); len(changed) != 1 || changed[0] != "f-north" {
		t.Fatalf("changed since r2: %v", changed)
	}
	if changed, _ := s.ChangedSince("mod-a", ""); len(changed) != 2 {
		t.Fatalf("an empty revision for an existing project means every file: %v", changed)
	}

	now += KeepReplacedMs + 1
	if _, err := s.Save(core.SaveRequest{Key: "mod-a", Files: []core.ProjectFile{{Name: "manifest", Content: `{"v":4}`}}, Deleted: []string{"f-north"}, Meta: core.ProjectMeta{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile("mod-a", "r1", "manifest"); !errors.Is(err, core.ErrRevisionUnknown) {
		t.Fatalf("a revision replaced over 10 minutes ago may go: %v", err)
	}
	if _, err := s.ReadFile("mod-a", "r4", "f-north"); !errors.Is(err, core.ErrFileUnknown) {
		t.Fatalf("deleted file: %v", err)
	}
}

func TestPlainDirectoryAndOddNames(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "seeded"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "seeded", "manifest"), []byte(`{"seed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New("fs", root, "sakhal", func() int64 { return 0 })
	list, _ := s.List()
	if len(list) != 1 || list[0].Revision != "r0" || list[0].ProjectName != "seeded" || list[0].Map != "sakhal" {
		t.Fatalf("list: %+v", list)
	}
	if _, err := s.Save(core.SaveRequest{Key: "k", Files: []core.ProjectFile{{Name: "../../evil", Content: "x"}, {Name: ".hidden", Content: "y"}}, Deleted: nil, Meta: core.ProjectMeta{}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".versions", "k", "r1"))
	if len(entries) != 2 {
		t.Fatalf("both files inside the version: %d", len(entries))
	}
	head, _ := s.Head("k", "")
	if head.Files[0].Name != "../../evil" || head.Files[1].Name != ".hidden" {
		t.Fatalf("names come back: %+v", head.Files)
	}
	if _, err := s.Save(core.SaveRequest{Key: "../x", Files: nil, Deleted: nil, Meta: core.ProjectMeta{}}); err == nil {
		t.Fatal("bad key refused")
	}
	if _, err := s.Head(".versions", ""); !errors.Is(err, core.ErrProjectUnknown) {
		t.Fatal("reserved key unknown")
	}
}
