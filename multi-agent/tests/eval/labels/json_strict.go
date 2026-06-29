// Strict JSON helper kept in a separate file so the test file does not need
// to import encoding/json directly (lets us localize DisallowUnknownFields-
// style strictness in one place if we later want it).
package labels

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func jsonUnmarshalStrict(b []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	// Reject trailing tokens so accidental concatenation surfaces — the
	// previous version swallowed the second Decode's error, defeating the
	// point of the check.
	if dec.More() {
		return fmt.Errorf("unexpected trailing token at offset %d", dec.InputOffset())
	}
	return nil
}
