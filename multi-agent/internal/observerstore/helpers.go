package observerstore

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func NowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func GeneratedEventID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func PrefixedID(prefix string) (string, error) {
	id, err := GeneratedEventID()
	if err != nil {
		return "", err
	}
	return prefix + "_" + id, nil
}

func NullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}
