package storage_test

import (
	"testing"

	"oro/pkg/storage"
)

func TestOroHomeAllowlistClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry storage.Entry
		want  storage.RetentionClass
	}{
		{name: "worker log", entry: storage.Entry{Path: "logs/worker.log"}, want: storage.RetentionLog},
		{name: "hook log", entry: storage.Entry{Path: "logs/hooks/session.log"}, want: storage.RetentionLog},
		{name: "rendered handoff", entry: storage.Entry{Path: "handoffs/project-a/rendered.md"}, want: storage.RetentionHandoff},
		{name: "database backup", entry: storage.Entry{Path: "backups/state.db.bak"}, want: storage.RetentionBackup},
		{name: "known temporary file", entry: storage.Entry{Path: "tmp/oro-cleanup-123.tmp"}, want: storage.RetentionTemporary},
		{name: "inactive wal", entry: storage.Entry{Path: "state.db-wal"}, want: storage.RetentionInactiveWAL},
		{name: "active log", entry: storage.Entry{Path: "logs/worker.log", Active: true}, want: storage.RetentionPreserve},
		{name: "active wal", entry: storage.Entry{Path: "state.db-wal", Active: true}, want: storage.RetentionPreserve},
		{name: "index database", entry: storage.Entry{Path: "indexes/code_index.db"}, want: storage.RetentionPreserve},
		{name: "index artifact", entry: storage.Entry{Path: "code_index.db"}, want: storage.RetentionPreserve},
		{name: "model", entry: storage.Entry{Path: "models/bge/model.onnx"}, want: storage.RetentionPreserve},
		{name: "configuration", entry: storage.Entry{Path: "config.yaml"}, want: storage.RetentionPreserve},
		{name: "task data", entry: storage.Entry{Path: "projects/oro/state.db"}, want: storage.RetentionPreserve},
		{name: "memory data", entry: storage.Entry{Path: "memory.db"}, want: storage.RetentionPreserve},
		{name: "card data", entry: storage.Entry{Path: "cards.db"}, want: storage.RetentionPreserve},
		{name: "unknown", entry: storage.Entry{Path: "unexpected/archive.tar"}, want: storage.RetentionPreserve},
		{name: "empty", entry: storage.Entry{}, want: storage.RetentionPreserve},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := storage.ClassifyOroHome(test.entry); got != test.want {
				t.Errorf("ClassifyOroHome(%#v) = %q, want %q", test.entry, got, test.want)
			}
		})
	}
}
