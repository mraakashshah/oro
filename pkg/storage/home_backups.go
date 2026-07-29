package storage

import (
	"path"
	"sort"
	"strings"
	"time"
)

const (
	// OroHomeBackupMaxAge is the maximum age retained for known database backups
	// beyond each database's newest backups.
	OroHomeBackupMaxAge = 7 * 24 * time.Hour
	// OroHomeBackupKeepNewest is the number of newest known backups retained
	// for each database regardless of age.
	OroHomeBackupKeepNewest = 3
)

// HomeBackup is one observed database recovery backup beneath the Oro home directory.
type HomeBackup struct {
	Path       string
	ModifiedAt time.Time
}

// PlanOroHomeBackupRetention selects only known database backups that are
// older than seven days and outside that database's newest three backups.
// Unknown or malformed backup formats are deliberately preserved.
//
//oro:testonly — production retention execution wiring lands in a subsequent task.
func PlanOroHomeBackupRetention(now time.Time, backups []HomeBackup) []HomeBackup {
	byDatabase := knownBackupsByDatabase(backups)
	selected := make([]HomeBackup, 0, len(backups))
	cutoff := now.Add(-OroHomeBackupMaxAge)
	for _, databaseBackups := range byDatabase {
		sort.Slice(databaseBackups, func(i, j int) bool {
			if databaseBackups[i].ModifiedAt.Equal(databaseBackups[j].ModifiedAt) {
				return databaseBackups[i].Path < databaseBackups[j].Path
			}
			return databaseBackups[i].ModifiedAt.After(databaseBackups[j].ModifiedAt)
		})
		for i, backup := range databaseBackups {
			if i >= OroHomeBackupKeepNewest && backup.ModifiedAt.Before(cutoff) {
				selected = append(selected, backup)
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })
	return selected
}

func knownBackupsByDatabase(backups []HomeBackup) map[string][]HomeBackup {
	byDatabase := make(map[string][]HomeBackup)
	for _, backup := range backups {
		canonical, database, ok := canonicalOroBackupPath(backup.Path)
		if !ok {
			continue
		}
		backup.Path = canonical
		byDatabase[database] = append(byDatabase[database], backup)
	}
	return byDatabase
}

func canonicalOroBackupPath(backupPath string) (canonical, database string, ok bool) {
	canonical = path.Clean(strings.TrimSpace(backupPath))
	if canonical == "." || strings.HasPrefix(canonical, "../") || strings.HasPrefix(canonical, "/") ||
		!strings.HasPrefix(canonical, "backups/") {
		return "", "", false
	}

	base := path.Base(canonical)
	if strings.HasSuffix(base, ".db.bak") {
		return canonical, strings.TrimSuffix(canonical, ".bak"), true
	}
	database, stamp, found := strings.Cut(canonical, ".bak-")
	if !found || !strings.HasSuffix(path.Base(database), ".db") {
		return "", "", false
	}
	if _, err := time.Parse("20060102-150405", stamp); err != nil {
		return "", "", false
	}
	return canonical, database, true
}
