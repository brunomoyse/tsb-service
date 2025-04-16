package db

import (
	"context"
	"fmt"

	"github.com/graph-gophers/dataloader"
)

// TypedLoader is a generic dataloader wrapper that works with type T.
type TypedLoader[T any] struct {
	Loader *dataloader.Loader
}

// BatchFetchFuncTyped is a function type that fetches data for a batch of keys,
// returning a map from key to a slice of type T.
type BatchFetchFuncTyped[T any] func(ctx context.Context, keys []string) (map[string][]T, error)

// NewTypedLoader creates a new TypedLoader using the provided batch fetch function.
func NewTypedLoader[T any](batchFetchFunc BatchFetchFuncTyped[T], errorMessage string) *TypedLoader[T] {
	batchFn := func(ctx context.Context, keys dataloader.Keys) []*dataloader.Result {
		ids := make([]string, len(keys))
		for i, key := range keys {
			ids[i] = key.String()
		}

		dataMap, err := batchFetchFunc(ctx, ids)
		results := make([]*dataloader.Result, len(keys))
		if err != nil {
			for i := range keys {
				results[i] = &dataloader.Result{
					Error: fmt.Errorf("%s: %w", errorMessage, err),
				}
			}
			return results
		}

		for i, key := range keys {
			if data, ok := dataMap[key.String()]; ok {
				results[i] = &dataloader.Result{
					Data: data, // data is of type []T
				}
			} else {
				results[i] = &dataloader.Result{
					Data: []T{},
				}
			}
		}
		return results
	}

	return &TypedLoader[T]{
		Loader: dataloader.NewBatchedLoader(
			batchFn,
			//nolint:mnd // default is 0, need to set a value
			dataloader.WithBatchCapacity(100),
		),
	}
}

// Load returns the loaded data for the given key.
func (tl *TypedLoader[T]) Load(ctx context.Context, key string) ([]T, error) {
	thunk := tl.Loader.Load(ctx, dataloader.StringKey(key))
	res, err := thunk()
	if err != nil {
		return nil, err
	}
	typed, ok := res.([]T)
	if !ok {
		return nil, fmt.Errorf("unexpected type from typed loader")
	}
	return typed, nil
}
