package repository

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The order status has exactly two SQL writers: OrderRepository.Update (reached only from
// orderService.UpdateOrder, the chokepoint the status observer hangs on) and
// OrderRepository.CancelStaleTestOrders (store-review test orders, which are never pushed anywhere).
// This guard reads the Go sources, so a third writer (a new UPDATE orders SET order_status, or a second
// caller of OrderRepository.Update) fails here and has to be wired through the service on purpose.
func TestOrderStatusHasExactlyTwoSQLWriters(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	var sources = map[string]string{}
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "generated.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			sources[filepath.ToSlash(rel)] = string(b)
			return nil
		}))
	}
	require.NotEmpty(t, sources)

	// An UPDATE of the orders table that assigns order_status, whatever the whitespace and case.
	writer := regexp.MustCompile(`(?is)UPDATE\s+orders\s+SET\b[^;` + "`" + `"]*?\border_status\s*=`)
	var writers []string
	for path, src := range sources {
		for range writer.FindAllString(src, -1) {
			writers = append(writers, path)
		}
	}
	assert.ElementsMatch(t,
		[]string{"internal/modules/order/infrastructure/repository.go", "internal/modules/order/infrastructure/repository.go"},
		writers, "OrderRepository.Update and CancelStaleTestOrders are the only SQL writers of orders.order_status")

	// OrderRepository.Update has a single caller: orderService.UpdateOrder.
	var callers []string
	call := regexp.MustCompile(`\brepo\.Update\(ctx, order\)`)
	for path, src := range sources {
		if call.MatchString(src) {
			callers = append(callers, path)
		}
	}
	assert.Equal(t, []string{"internal/modules/order/application/service.go"}, callers)
	assert.Equal(t, 1, len(call.FindAllString(sources["internal/modules/order/application/service.go"], -1)))
}
