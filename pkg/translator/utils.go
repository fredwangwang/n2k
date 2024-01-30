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

func AbbrevStr(in string, length int) string {
	if len(in) < length {
		return in
	}
	return in[:length] + "..."
}
