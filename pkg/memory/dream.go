package memory

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DreamAction represents a single memory mutation produced by the dream process.
// Kind is one of "DELETE", "CREATE", or "MERGE".
type DreamAction struct {
	Kind   string       // "DELETE" | "CREATE" | "MERGE"
	ID     int64        // set for DELETE
	IDs    []int64      // set for MERGE (source IDs to remove)
	Params InsertParams // set for CREATE and MERGE (content of new/merged memory)
}

// dreamDeleteRe matches: [DELETE] <id>
var dreamDeleteRe = regexp.MustCompile(`^\[DELETE\]\s+(\d+)$`)

// dreamCreateRe matches: [CREATE] type=<type>[ tags=<t1,t2>]: <content>
var dreamCreateRe = regexp.MustCompile(`^\[CREATE\]\s+type=(\w+)(?:\s+tags=([^\s:]+))?:\s+(.+)$`)

// dreamMergeRe matches: [MERGE] <id1> <id2> type=<type>[ tags=<t1,t2>]: <content>
var dreamMergeRe = regexp.MustCompile(`^\[MERGE\]\s+(\d+)\s+(\d+)\s+type=(\w+)(?:\s+tags=([^\s:]+))?:\s+(.+)$`)

// ParseDreamActions scans output line-by-line and extracts dream actions.
// Malformed lines are silently skipped.
// An empty output string returns zero actions.
func ParseDreamActions(output string) []DreamAction {
	var actions []DreamAction
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if a, ok := parseDreamLine(line); ok {
			actions = append(actions, a)
		}
	}
	return actions
}

// parseDreamLine attempts to parse a single line into a DreamAction.
func parseDreamLine(line string) (DreamAction, bool) {
	if m := dreamDeleteRe.FindStringSubmatch(line); m != nil {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return DreamAction{}, false
		}
		return DreamAction{Kind: "DELETE", ID: id}, true
	}

	if m := dreamCreateRe.FindStringSubmatch(line); m != nil {
		params := InsertParams{
			Type:    m[1],
			Content: m[3],
			Source:  "dreamer",
		}
		if m[2] != "" {
			params.Tags = strings.Split(m[2], ",")
		}
		return DreamAction{Kind: "CREATE", Params: params}, true
	}

	if m := dreamMergeRe.FindStringSubmatch(line); m != nil {
		id1, err1 := strconv.ParseInt(m[1], 10, 64)
		id2, err2 := strconv.ParseInt(m[2], 10, 64)
		if err1 != nil || err2 != nil {
			return DreamAction{}, false
		}
		params := InsertParams{
			Type:    m[3],
			Content: m[5],
			Source:  "dreamer",
		}
		if m[4] != "" {
			params.Tags = strings.Split(m[4], ",")
		}
		return DreamAction{Kind: "MERGE", IDs: []int64{id1, id2}, Params: params}, true
	}

	return DreamAction{}, false
}

// ExecuteActions applies a slice of DreamActions against the store.
// Store errors are logged via logFn and execution continues to remaining actions.
// The function always returns nil — errors are surfaced through logFn only.
func ExecuteActions(ctx context.Context, actions []DreamAction, store *Store, logFn func(string)) error {
	for _, a := range actions {
		switch a.Kind {
		case "DELETE":
			if err := store.Delete(ctx, a.ID); err != nil {
				logFn(fmt.Sprintf("dream execute: delete %d: %v", a.ID, err))
			}
		case "CREATE":
			if _, err := store.Insert(ctx, a.Params); err != nil {
				logFn(fmt.Sprintf("dream execute: create: %v", err))
			}
		case "MERGE":
			// Insert the merged memory and delete each source atomically.
			if _, err := store.executeMergeAtomic(ctx, a.Params, a.IDs); err != nil {
				logFn(fmt.Sprintf("dream execute: merge: %v", err))
			}
		default:
			logFn(fmt.Sprintf("dream execute: unknown action kind %q", a.Kind))
		}
	}
	return nil
}
