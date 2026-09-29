package workflow

import (
	"os"
	"strings"
	"testing"
)

func TestQualityActionPinOnlyException(t *testing.T) {
	caller, err := os.ReadFile("../../.github/workflows/pr-fast.yml")
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile("../../.github/workflows/quality.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	const oldPin = "actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5.0.0"
	const newPin = "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0"
	candidate := []byte(strings.Replace(string(base), oldPin, newPin, 1))
	if string(candidate) == string(base) {
		t.Fatal("test fixture has no pin change")
	}
	var verified []ActionPin
	verify := func(pin ActionPin) error { verified = append(verified, pin); return nil }
	if err := CheckSofaQualityContractWithActionPins(caller, candidate, base, verify); err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || verified[0] != (ActionPin{Name: "actions/setup-node", SHA: "820762786026740c76f36085b0efc47a31fe5020", Tag: "v7.0.0"}) {
		t.Fatalf("unexpected provenance checks: %+v", verified)
	}
	for name, changed := range map[string]string{
		"command":     strings.Replace(string(candidate), "go test -count=1 ./...", "true", 1),
		"permissions": strings.Replace(string(candidate), "contents: read", "contents: write", 1),
		"new step":    string(candidate) + "\n# extra command\n",
		"fake tag":    strings.Replace(string(candidate), "# v7.0.0", "# latest", 1),
		"fake action": strings.Replace(string(candidate), "actions/setup-node", "attacker/setup-node", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := CheckSofaQualityContractWithActionPins(caller, []byte(changed), base, verify); err == nil {
				t.Fatal("unapproved workflow change accepted")
			}
		})
	}
	if err := CheckSofaQualityContractWithActionPins(caller, candidate, base, nil); err == nil {
		t.Fatal("pin update accepted without release verification")
	}
}
