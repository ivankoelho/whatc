package testutil

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ScratchDatabase makes a brand new EMPTY database on the test server (the one TEST_DATABASE_URL points at) and drops
// it when the test ends. A test that must break or recreate structures (an index, a table) uses it so that it never
// touches the shared test database or any existing one, and can neither disturb nor be disturbed by another test.
func ScratchDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping database test")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	name := "whatomate_scratch_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	admin := *u
	admin.Path = "/postgres"
	adm, err := gorm.Open(postgres.Open(admin.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, adm.Exec("CREATE DATABASE "+name).Error)
	scratch := *u
	scratch.Path = "/" + name
	db, err := gorm.Open(postgres.Open(scratch.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
		_ = adm.Exec("DROP DATABASE IF EXISTS " + name).Error
		if sqlDB, e := adm.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
