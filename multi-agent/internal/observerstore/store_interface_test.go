package observerstore

import "testing"

func TestSQLiteStoreImplementsStoreInterface(t *testing.T) {
	var _ Store = (*SQLiteStore)(nil)
}
