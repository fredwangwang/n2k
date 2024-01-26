package translator

import (
	"reflect"
)

func AppendIfNotNil[T any](arr []T, v T) []T {
	if reflect.ValueOf(&v).Elem().IsZero() {
		return arr
	}

	return append(arr, v)
}
