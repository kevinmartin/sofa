package distribution

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestReproducibleBundleAndMetadataValidation(t *testing.T) {
	p := fixturePlan()
	first := fixtureBundle(t, p)
	second := fixtureBundle(t, p)
	if !bytes.Equal(first, second) {
		t.Fatal("rebuilt bundle differs")
	}
	if err := ValidateBundle(p, first); err != nil {
		t.Fatal(err)
	}
	corrupted := append([]byte(nil), first...)
	corrupted[len(corrupted)-8] ^= 0x80
	if err := ValidateBundle(p, corrupted); err == nil {
		t.Fatal("corrupted gzip checksum accepted before publication")
	}
	changed := p
	changed.SourceSHA = otherSource
	if err := ValidateBundle(changed, first); err == nil {
		t.Fatal("wrong source metadata accepted")
	}
	bad, err := Bundle(p, map[string][]byte{"sofa": []byte("not ELF"), "sofa-test": elfFixture()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBundle(p, bad); err == nil {
		t.Fatal("nonbinary fixture accepted")
	}
	var metadata Metadata
	if json.Unmarshal(metadataBytes(p), &metadata) != nil || metadata != MetadataFor(p) {
		t.Fatal("standalone metadata differs")
	}
}
