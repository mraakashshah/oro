package beadstore

import "strings"

// ExtractAndStripAC extracts an Acceptance Criteria markdown section from a
// description and returns the remaining description without that section.
func ExtractAndStripAC(description string) (string, string, error) {
	return extractAndStripAC(description)
}

func extractAndStripAC(description string) (string, string, error) {
	idx, headerLen := findACHeader(description)
	if idx < 0 {
		return "", description, nil
	}

	bodyStart := idx + headerLen
	for bodyStart < len(description) && (description[bodyStart] == '\r' || description[bodyStart] == '\n') {
		bodyStart++
	}

	body := description[bodyStart:]
	acEnd := len(body)
	suffix := ""
	if next := strings.Index(body, "\n## "); next >= 0 {
		acEnd = next
		suffix = body[next+1:]
	}

	ac := strings.Trim(body[:acEnd], " \t\r\n")
	desc := strings.TrimRight(description[:idx], " \t\r\n")
	if suffix != "" {
		if desc != "" {
			desc += "\n\n"
		}
		desc += strings.TrimLeft(suffix, "\r\n")
	}
	desc = strings.TrimRight(desc, " \t\r\n")

	return ac, desc, nil
}

func findACHeader(description string) (int, int) {
	lower := strings.ToLower(description)
	headers := []string{"## acceptance criteria", "acceptance criteria"}
	for _, header := range headers {
		if strings.HasPrefix(lower, header) {
			return 0, len(header)
		}
		if idx := strings.Index(lower, "\n"+header); idx >= 0 {
			return idx + 1, len(header)
		}
	}
	return -1, 0
}
