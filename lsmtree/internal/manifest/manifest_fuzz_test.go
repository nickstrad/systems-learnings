package manifest_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/manifest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
)

// FuzzManifestDecode feeds Decode arbitrary bytes (see checkDecode).
func FuzzManifestDecode(f *testing.F) {
	for _, v := range []meta.Version{{}, oldVersion, newVersion} {
		f.Add(manifest.Encode(v))
	}
	f.Add([]byte{})
	f.Add([]byte("crc32c 00000000\n"))
	f.Add(manifest.Seal([]byte(`{"nextFileNum": 5, "lastSeq": 3, "levels": [[{"num": 1, "smallest": "00", "largest": "ff", "maxSeq": 3}]]}`)))
	f.Add(manifest.Seal([]byte(`{"levels": [null, [], [{}]]}`)))
	f.Add(manifest.Seal([]byte(`{"nextFileNum": 1, "levels": [[], []], "cursors": ["", "00ff"]}`)))
	f.Fuzz(checkDecode)
}

// FuzzManifestBody is FuzzManifestDecode with a valid checksum added to
// every input. Random bytes almost never pass the CRC, so without this
// the fuzzer would rarely reach the JSON and invariant checks behind it.
func FuzzManifestBody(f *testing.F) {
	for _, v := range []meta.Version{{}, oldVersion, newVersion} {
		b := manifest.Encode(v)
		f.Add(b[:len(b)-len("crc32c 00000000\n")])
	}
	f.Add([]byte(`{"levels": [null, [], [{}]]}`))
	f.Add([]byte(`{"nextFileNum": 1, "levels": [[], []], "cursors": ["", "00ff", ""]}`))
	f.Fuzz(func(t *testing.T, body []byte) { checkDecode(t, manifest.Seal(body)) })
}

// checkDecode is the property both targets check: Decode never panics,
// fails only with ErrCorrupt, and whatever it accepts is a valid Version
// that encodes and decodes back to itself.
func checkDecode(t *testing.T, b []byte) {
	v, err := manifest.Decode(b)
	if err != nil {
		if !errors.Is(err, manifest.ErrCorrupt) {
			t.Fatalf("Decode error %v does not wrap ErrCorrupt", err)
		}
		return
	}
	v.CheckInvariant() // with assertions on, a bad accept panics here
	enc := manifest.Encode(v)
	again, err := manifest.Decode(enc)
	if err != nil {
		t.Fatalf("Decode(Encode(v)) of an accepted manifest: %v", err)
	}
	if !sameVersion(again, v) || !bytes.Equal(manifest.Encode(again), enc) {
		t.Fatalf("re-encoding changed the version:\n got %+v\nwant %+v", again, v)
	}
}
