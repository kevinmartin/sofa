package workflow

import (
	"errors"
	"regexp"
	"strings"
)

// ActionPin is an exact release identity for one of the first-party actions
// permitted to change without a separate quality-gate digest approval.
type ActionPin struct {
	Name string
	SHA  string
	Tag  string
}

var actionPinLine = regexp.MustCompile(`^([ \t]*(?:- )?uses: )(actions/(?:checkout|setup-go|setup-node))@([0-9a-f]{40}) # (v[0-9]+\.[0-9]+\.[0-9]+)\n?$`)

// ActionPinChanges compares raw workflow bytes. It accepts only replacements
// of the SHA and release comment on an existing, allowlisted action line.
// Everything else, including whitespace outside those fields, is immutable.
func ActionPinChanges(base, candidate []byte) ([]ActionPin, error) {
	if len(base) == 0 || len(base) > 128<<10 || len(candidate) == 0 || len(candidate) > 128<<10 {
		return nil, errors.New("quality workflow size invalid")
	}
	before, after := strings.SplitAfter(string(base), "\n"), strings.SplitAfter(string(candidate), "\n")
	if len(before) != len(after) {
		return nil, errors.New("quality workflow changed beyond action pins")
	}
	var pins []ActionPin
	seen := make(map[ActionPin]bool)
	for i, line := range before {
		if line == after[i] {
			continue
		}
		old, next := actionPinLine.FindStringSubmatch(line), actionPinLine.FindStringSubmatch(after[i])
		if old == nil || next == nil || old[1] != next[1] || old[2] != next[2] || strings.HasSuffix(line, "\n") != strings.HasSuffix(after[i], "\n") {
			return nil, errors.New("quality workflow changed beyond action pins")
		}
		pin := ActionPin{Name: next[2], SHA: next[3], Tag: next[4]}
		if !seen[pin] {
			pins = append(pins, pin)
			seen[pin] = true
		}
	}
	if len(pins) == 0 {
		return nil, errors.New("quality workflow has no reviewed action pin changes")
	}
	return pins, nil
}
