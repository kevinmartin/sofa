// Package discovery implements the specification and owner-approval boundary.
// Issue text is untrusted research input; Project status is observed authority.
package discovery

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const SpecificationVersion = 1

const marker = "<!-- sofa:specification v1 -->"
const publicationKeyPrefix = "<!-- sofa:publication-key="

var keyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var headings = []string{
	"Problem and intended user",
	"Evidence and reproducer",
	"Goals",
	"Non-goals",
	"Constraints and affected components",
	"Dependencies",
	"Acceptance examples",
	"Proposed validation",
	"Risks",
	"Unanswered questions",
	"Delivery slices",
}

// Specification is the owner-visible, versioned scope. Additional evidence
// can be linked in its evidence section, but any change to this document
// changes its canonical digest and requires another Project approval.
type Specification struct {
	Version        int
	Problem        string
	Evidence       string
	Goals          string
	NonGoals       string
	Constraints    string
	Dependencies   string
	Acceptance     string
	Validation     string
	Risks          string
	Questions      string
	DeliverySlices string
}

func (s Specification) fields() []string {
	return []string{s.Problem, s.Evidence, s.Goals, s.NonGoals, s.Constraints, s.Dependencies, s.Acceptance, s.Validation, s.Risks, s.Questions, s.DeliverySlices}
}

func (s Specification) Validate() error {
	if s.Version != SpecificationVersion {
		return errors.New("unsupported specification version")
	}
	total := 0
	for i, value := range s.fields() {
		value = strings.TrimSpace(value)
		total += len(value)
		if value == "" || len(value) > 16<<10 || !utf8.ValidString(value) || strings.Contains(value, marker) || strings.Contains(value, publicationKeyPrefix) {
			return fmt.Errorf("invalid specification section %q", headings[i])
		}
		for line := range strings.SplitSeq(value, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "## ") {
				return fmt.Errorf("nested specification section in %q", headings[i])
			}
		}
	}
	if total > 60<<10 {
		return errors.New("specification exceeds size limit")
	}
	return nil
}

// WithPublicationKey adds a deterministic, non-authorizing retry locator. It
// does not change the human specification fields, but is part of its digest.
func WithPublicationKey(body, key string) (string, error) {
	if !keyPattern.MatchString(key) {
		return "", errors.New("invalid Discovery publication key")
	}
	if _, err := Parse(body); err != nil {
		return "", err
	}
	if strings.Contains(body, publicationKeyPrefix) {
		return "", errors.New("Discovery publication key already present")
	}
	return strings.Replace(body, marker, marker+"\n"+publicationKeyPrefix+key+" -->", 1), nil
}

func PublicationKey(body string) string {
	start := strings.Index(body, publicationKeyPrefix)
	if start < 0 {
		return ""
	}
	value := strings.TrimPrefix(body[start:], publicationKeyPrefix)
	value, _, found := strings.Cut(value, " -->")
	if !found || !keyPattern.MatchString(value) {
		return ""
	}
	return value
}

// Render returns a stable issue body. Approval also binds the issue title via
// admission.CanonicalSpec, so a title change cannot silently inherit approval.
func (s Specification) Render() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString("\n\n")
	for i, value := range s.fields() {
		b.WriteString("## ")
		b.WriteString(headings[i])
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}

// Parse rejects a partial or ambiguous body; a general issue description is
// not an approved Discovery specification merely because it entered Backlog.
func Parse(body string) (Specification, error) {
	var empty Specification
	if !utf8.ValidString(body) || len(body) > 64<<10 {
		return empty, errors.New("invalid specification body")
	}
	canonical := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n"))
	if !strings.HasPrefix(canonical, marker+"\n") {
		return empty, errors.New("versioned specification marker missing")
	}
	rest := strings.TrimSpace(strings.TrimPrefix(canonical, marker))
	if strings.HasPrefix(rest, publicationKeyPrefix) {
		line, after, found := strings.Cut(rest, "\n")
		if !found || !strings.HasSuffix(line, " -->") || !keyPattern.MatchString(strings.TrimSuffix(strings.TrimPrefix(line, publicationKeyPrefix), " -->")) {
			return empty, errors.New("invalid Discovery publication key")
		}
		rest = strings.TrimSpace(after)
	}
	values := make([]string, len(headings))
	for i, heading := range headings {
		prefix := "## " + heading + "\n"
		if !strings.HasPrefix(rest, prefix) {
			return empty, fmt.Errorf("specification section %q missing or reordered", heading)
		}
		rest = strings.TrimPrefix(rest, prefix)
		if i+1 < len(headings) {
			next := "\n## " + headings[i+1] + "\n"
			position := strings.Index(rest, next)
			if position < 0 {
				return empty, fmt.Errorf("specification section %q missing", headings[i+1])
			}
			values[i] = strings.TrimSpace(rest[:position])
			rest = strings.TrimSpace(rest[position:])
		} else {
			values[i] = strings.TrimSpace(rest)
		}
	}
	s := Specification{
		Version:        SpecificationVersion,
		Problem:        values[0],
		Evidence:       values[1],
		Goals:          values[2],
		NonGoals:       values[3],
		Constraints:    values[4],
		Dependencies:   values[5],
		Acceptance:     values[6],
		Validation:     values[7],
		Risks:          values[8],
		Questions:      values[9],
		DeliverySlices: values[10],
	}
	return s, s.Validate()
}
