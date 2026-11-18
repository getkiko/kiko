package schema_test

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model/tsgolden"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

func arg[T any](c tsgolden.Call, i int) T {
	var v T
	c.Arg(i, &v)
	return v
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type signalAddress struct {
	Server   string `json:"server"`
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
	Path     string `json:"path,omitempty"`
	Scheme   string `json:"scheme,omitempty"`
	IsDomain bool   `json:"isDomain"`
}

var schemaAdapters = map[string]tsgolden.Adapter{
	"schema.endpointAddress": func(c tsgolden.Call) (any, error) {
		return schema.EndpointAddress(arg[string](c, 0), arg[map[string]any](c, 1)), nil
	},
	"schema.normalizeUsbDevices": func(c tsgolden.Call) (any, error) {
		return schema.NormalizeUsbDevices(arg[any](c, 0)), nil
	},
	"schema.usbDevicesError": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(schema.UsbDevicesError(arg[string](c, 0), arg[map[string]any](c, 1))), nil
	},
	"schema.serviceUnavailableReason": func(c tsgolden.Call) (any, error) {
		return schema.ServiceUnavailableReason(arg[string](c, 0), arg[string](c, 1)), nil
	},
	"schema.isCidr": func(c tsgolden.Call) (any, error) { return schema.IsCIDR(arg[string](c, 0)), nil },
	"holepunch.isIPAddress": func(c tsgolden.Call) (any, error) {
		return schema.IsIPAddress(arg[string](c, 0)), nil
	},
	"holepunch.parseHolepunchSignalAddress": func(c tsgolden.Call) (any, error) {
		v, problem := schema.ParseBurrowSignalAddress(arg[string](c, 0))
		type result struct {
			Value *signalAddress `json:"value,omitempty"`
			Error string         `json:"error,omitempty"`
		}
		if problem != "" {
			return result{Error: problem}, nil
		}
		return result{Value: &signalAddress{v.Server, v.Hostname, v.Port, v.Path, v.Scheme, v.IsDomain}}, nil
	},
	"holepunch.formatHolepunchSignalAddress": func(c tsgolden.Call) (any, error) {
		return schema.FormatBurrowSignalAddress(arg[any](c, 0), arg[any](c, 1), arg[any](c, 2)), nil
	},
	"holepunch.holepunchConfigFromFields": func(c tsgolden.Call) (any, error) {
		out := map[string]any{}
		for _, kv := range schema.BurrowConfigFromFields(arg[map[string]any](c, 0)) {
			out[kv[0].(string)] = kv[1]
		}
		return out, nil
	},
}

// TestTypeScriptParity replays the schema calls recorded from the former
// TypeScript console (see package tsgolden).
func TestTypeScriptParity(t *testing.T) {
	calls := tsgolden.Load(t, "testdata/ts-calls.jsonl.gz")
	ran := tsgolden.Replay(t, calls, schemaAdapters, tsgolden.Options{})
	tsgolden.Summary(t, ran, schemaAdapters)
}
