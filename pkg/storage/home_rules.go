package storage

import (
	"path"
	"strings"
)

// Entry is one observed path beneath the Oro home directory.
type Entry struct {
	Path   string
	Active bool
}

// RetentionClass identifies whether an Oro-home entry belongs to the explicit
// cleanup allowlist or must be preserved.
type RetentionClass string

const (
	// RetentionPreserve protects durable and unknown Oro-home paths.
	RetentionPreserve RetentionClass = "preserve"
	// RetentionLog identifies an inactive worker or hook log.
	RetentionLog RetentionClass = "log"
	// RetentionHandoff identifies a rendered handoff.
	RetentionHandoff RetentionClass = "handoff"
	// RetentionBackup identifies a database recovery backup.
	RetentionBackup RetentionClass = "backup"
	// RetentionTemporary identifies a known Oro temporary file.
	RetentionTemporary RetentionClass = "temporary"
	// RetentionInactiveWAL identifies a WAL eligible for SQLite checkpointing.
	RetentionInactiveWAL RetentionClass = "inactive_wal"
)

// ClassifyOroHome assigns only explicit disposable Oro-home paths to a
// cleanable retention class. Unknown paths and active entries are preserved.
//
//oro:testonly — production cleanup planner wiring lands in a subsequent storage lifecycle task.
func ClassifyOroHome(entry Entry) RetentionClass {
	cleaned := path.Clean(strings.TrimSpace(entry.Path))
	if entry.Active || cleaned == "." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return RetentionPreserve
	}

	switch {
	case isOroLog(cleaned):
		return RetentionLog
	case isRenderedHandoff(cleaned):
		return RetentionHandoff
	case isRecoveryBackup(cleaned):
		return RetentionBackup
	case isKnownTemporary(cleaned):
		return RetentionTemporary
	case strings.HasSuffix(cleaned, ".db-wal"):
		return RetentionInactiveWAL
	default:
		return RetentionPreserve
	}
}

func isOroLog(entryPath string) bool {
	return strings.HasPrefix(entryPath, "logs/") && strings.HasSuffix(entryPath, ".log")
}

func isRenderedHandoff(entryPath string) bool {
	return strings.HasPrefix(entryPath, "handoffs/") && strings.HasSuffix(entryPath, ".md")
}

func isRecoveryBackup(entryPath string) bool {
	return strings.HasPrefix(entryPath, "backups/") && strings.HasSuffix(entryPath, ".bak")
}

func isKnownTemporary(entryPath string) bool {
	return strings.HasPrefix(entryPath, "tmp/oro-") && (strings.HasSuffix(entryPath, ".tmp") || strings.HasSuffix(entryPath, ".partial"))
}
