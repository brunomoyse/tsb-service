package repository

import (
	"os"
	"testing"

	"tsb-service/internal/api/graphql/testhelpers"
)

// TestMain removes the package's shared PostgreSQL container once its tests are done.
func TestMain(m *testing.M) { os.Exit(testhelpers.Main(m)) }
