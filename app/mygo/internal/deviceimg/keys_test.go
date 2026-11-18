package deviceimg_test

import (
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/deviceimg"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// model keeps its own copy of the built-in keys; both must agree.
func TestModelKeysMatch(t *testing.T) {
	if !slices.Equal(deviceimg.Keys(), model.DeviceImageKeys) {
		t.Fatalf("deviceimg %v, model %v", deviceimg.Keys(), model.DeviceImageKeys)
	}
}
