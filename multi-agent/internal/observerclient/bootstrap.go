package observerclient

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
)

// writeTokenFile writes the plaintext token to path with mode 0600, replacing
// any existing content (O_WRONLY|O_CREATE|O_TRUNC). The parent directory must
// already exist; this is the caller's responsibility (validated at config load).
func writeTokenFile(path, token string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(token); err != nil {
		return err
	}
	return f.Sync()
}

// readTokenFile reads the token from path. ok=false means the file does not
// exist or contains only whitespace; in that case err is nil. Any other I/O
// error is surfaced via err.
func readTokenFile(path string) (token string, ok bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	trimmed := string(bytes.TrimSpace(data))
	if trimmed == "" {
		return "", false, nil
	}
	return trimmed, true, nil
}
