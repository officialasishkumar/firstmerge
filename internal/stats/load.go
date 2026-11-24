// Package stats turns collected pull requests into the first-timer report.
package stats

import (
	"bufio"
	"encoding/json"
	"os"

	"github.com/officialasishkumar/firstmerge/internal/gh"
)

// Load reads a JSON Lines file written by collect.
func Load(path string) ([]gh.PR, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []gh.PR
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<24)
	for s.Scan() {
		var p gh.PR
		if err := json.Unmarshal(s.Bytes(), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, s.Err()
}
