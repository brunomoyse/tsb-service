package migrationtest_test

import (
	"os"
	"testing"

	"tsb-service/internal/api/graphql/testhelpers"
)

func TestMain(m *testing.M) { os.Exit(testhelpers.Main(m)) }
