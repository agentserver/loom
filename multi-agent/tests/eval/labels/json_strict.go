// Strict JSON helper kept in a separate file so the test file does not need
// to import encoding/json directly (lets us localize DisallowUnknownFields-
// style strictness in one place if we later want it).
package labels

import (
	"bytes"
	"encoding/json"
)

func jsonUnmarshalStrict(b []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	// Reject trailing tokens so accidental concatenation surfaces.
	if dec.More() {
		// Drain to produce a meaningful error.
		var tail any
		_ = dec.Decode(&tail)
	}
	return nil
}
