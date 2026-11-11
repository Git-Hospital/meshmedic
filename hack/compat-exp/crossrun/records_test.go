package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRec(t *testing.T, root, dir, file, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The records directory has the layout of the report artifact: exp-<run>-<chart>-<world>/<chart>.<world>.run<N>.json.
func TestLoadRecordsLayout(t *testing.T) {
	const rec = `{"chart":"redis-fix","world":"W3","run":2}`
	good := func(t *testing.T) string {
		root := t.TempDir()
		writeRec(t, root, "exp-77-redis-fix-W3", "redis-fix.W3.run2.json", rec)
		return root
	}
	if recs, err := loadRecords(good(t), "77"); err != nil || len(recs) != 1 || recs[0].Chart != "redis-fix" {
		t.Fatalf("%v %v", recs, err)
	}
	cases := []struct {
		name string
		edit func(root string)
		want string
	}{
		{"flat directory", func(r string) {
			_ = os.WriteFile(filepath.Join(r, "redis-fix.W3.run2.json"), []byte(rec), 0o644)
		}, "not a directory named exp-"},
		{"another run's directory", func(r string) {
			writeRec(t, r, "exp-78-redis-W1", "redis.W1.run1.json", `{"chart":"redis","world":"W1","run":1}`)
		}, "belongs to run 78"},
		{"extra file", func(r string) { writeRec(t, r, "exp-77-redis-fix-W3", "notes.txt", "x") }, "unexpected entry"},
		{"run index out of range", func(r string) { writeRec(t, r, "exp-77-redis-fix-W3", "redis-fix.W3.run4.json", rec) }, "unexpected entry"},
		{"file of another chart in the directory", func(r string) { writeRec(t, r, "exp-77-redis-fix-W3", "valkey.W3.run1.json", rec) }, "the directory says"},
		{"record fields of another chart", func(r string) {
			writeRec(t, r, "exp-77-redis-fix-W3", "redis-fix.W3.run1.json", `{"chart":"valkey","world":"W3","run":1}`)
		}, "the record says"},
		{"record of another world", func(r string) {
			writeRec(t, r, "exp-77-redis-fix-W3", "redis-fix.W3.run1.json", `{"chart":"redis-fix","world":"W1","run":1}`)
		}, "the record says"},
		{"symbolic link", func(r string) {
			_ = os.Symlink("/etc/hosts", filepath.Join(r, "exp-77-redis-fix-W3", "redis-fix.W3.run3.json"))
		}, "unexpected entry"},
		{"symbolic link as the directory", func(r string) {
			_ = os.Symlink(os.TempDir(), filepath.Join(r, "exp-77-x-W1"))
		}, "not a directory named exp-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := good(t)
			tc.edit(root)
			if _, err := loadRecords(root, "77"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	t.Run("no records at all", func(t *testing.T) {
		if _, err := loadRecords(t.TempDir(), "77"); err == nil || !strings.Contains(err.Error(), "no records found") {
			t.Fatal(err)
		}
	})
}
