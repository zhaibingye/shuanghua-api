package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"testing"
)

// Each startup opens a new pool. Close every pool before TempDir cleanup,
// including both passes of the migration idempotency fixtures.
func initControllerTestDatabase(t *testing.T, logDatabase bool) error {
	t.Helper()
	var err error
	if logDatabase {
		err = model.InitLogDB()
	} else {
		err = model.InitDB()
	}
	if err != nil {
		return err
	}
	db := model.DB
	if logDatabase {
		db = model.LOG_DB
	}
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	return nil
}
