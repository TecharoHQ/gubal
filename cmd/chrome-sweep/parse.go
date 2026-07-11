package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
)

// parseVersions trims and de-empties the given tags, rejects duplicates, and
// errors if nothing usable remains.
func parseVersions(args []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(args))
	for _, a := range args {
		t := strings.TrimSpace(a)
		if t == "" {
			continue
		}
		if seen[t] {
			return nil, fmt.Errorf("duplicate version %q", t)
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no versions given")
	}
	return out, nil
}

// reportedUA returns the User-Agent value the smoke container logged, or "".
func reportedUA(smokeLogs string) string {
	const marker = "User-Agent: "
	ua := ""
	sc := bufio.NewScanner(strings.NewReader(smokeLogs))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, marker); i >= 0 {
			ua = strings.TrimSpace(line[i+len(marker):])
		}
	}
	return ua
}

// capturedFramePath scans chrome-bully's JSON log lines for the frame it wrote,
// returning that path. A "fatal" line becomes an error; absence of both is an error.
func capturedFramePath(bullyLogs string) (string, error) {
	type logLine struct {
		Msg  string `json:"msg"`
		Path string `json:"path"`
		Err  string `json:"err"`
	}
	sc := bufio.NewScanner(strings.NewReader(bullyLogs))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var l logLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue // non-JSON line; skip
		}
		switch l.Msg {
		case "captured":
			if l.Path != "" {
				return l.Path, nil
			}
		case "fatal":
			return "", fmt.Errorf("chrome-bully failed: %s", l.Err)
		}
	}
	return "", fmt.Errorf("no captured frame in chrome-bully logs")
}
