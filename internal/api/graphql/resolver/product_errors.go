package resolver

import (
	"errors"
	"fmt"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/modules/product/domain"
)

// productWriteFailure maps a duplicate product name to a readable USER_ERROR, anything else stays an
// internal error.
func productWriteFailure(action string, err error) error {
	if errors.Is(err, domain.ErrDuplicateProductName) {
		return apperr.New(apperr.CodeUserError, "a product with this name already exists in this category, choose another name")
	}
	return fmt.Errorf("%s: %w", action, err)
}
