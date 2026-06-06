package postgres

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run PostgreSQL integration tests")
	}
	return dsn
}

func TestMigrateCreatesCoreTables(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	var exists bool
	err = st.db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'events'
	)`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists)
}
