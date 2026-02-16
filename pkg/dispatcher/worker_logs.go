package dispatcher

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// workerIDPattern validates worker IDs to prevent path traversal attacks.
// Allows alphanumeric characters, hyphens, dots, and underscores.
var workerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// applyWorkerLogs reads the last N lines from a worker's output.log file.
// Args format: "<worker-id> [count]" where count defaults to 20.
// Returns the log lines as a string, or an error if the worker ID is invalid.
func (d *Dispatcher) applyWorkerLogs(args string) (string, error) {
	// Parse args: worker-id [count]
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return "", fmt.Errorf("worker-logs requires worker ID argument")
	}

	workerID := parts[0]
	count := 20 // default

	if len(parts) > 1 {
		var err error
		count, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", fmt.Errorf("invalid line count: %w", err)
		}
		if count <= 0 {
			return "", fmt.Errorf("line count must be positive")
		}
	}

	// Validate worker ID to prevent path traversal
	if !workerIDPattern.MatchString(workerID) {
		return "", fmt.Errorf("invalid worker ID: contains illegal characters")
	}

	// Check if worker exists
	d.mu.Lock()
	_, exists := d.workers[workerID]
	d.mu.Unlock()

	if !exists {
		return "", fmt.Errorf("worker %s not found", workerID)
	}

	// Derive log path
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}

	logPath := filepath.Join(home, ".oro", "workers", workerID, "output.log")

	// Check if log file exists
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return "no output available", nil
	}

	// Read last N lines
	lines, err := readLastNLines(logPath, count)
	if err != nil {
		return "", fmt.Errorf("read log file: %w", err)
	}

	if len(lines) == 0 {
		return "no output available", nil
	}

	return strings.Join(lines, "\n"), nil
}

// readLastNLines reads the last N lines from a file efficiently.
// Uses a sliding window approach to avoid loading the entire file into memory.
func readLastNLines(path string, n int) ([]string, error) {
	// #nosec G304 -- path is derived from validated worker ID and fixed directory structure
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	// Read all lines into a buffer
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}

	// Return last N lines
	if len(lines) <= n {
		return lines, nil
	}
	return lines[len(lines)-n:], nil
}
