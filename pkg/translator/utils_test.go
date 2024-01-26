package translator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppendIfNotNil(t *testing.T) {
	arri := []*int{}

	var ip *int

	arri = AppendIfNotNil(arri, ip)
	assert.True(t, len(arri) == 0)
}
