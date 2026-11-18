package emit_test

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func scriptDoc(scripts ...*model.HTTPScript) *model.Doc {
	d := bareDoc()
	d.HTTPTools = &model.HTTPTools{Scripts: scripts}
	return d
}

func bareDoc() *model.Doc {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	d.Experimental.ClashAPI, d.Experimental.CacheFile = false, false
	return d
}

func TestEmitScriptsAbsentWithoutScripts(t *testing.T) {
	d := bareDoc()
	r := emit.Emit(d, emit.Options{})
	if _, ok := r.Config["scripts"]; ok {
		t.Fatal("scripts emitted without scripts")
	}
	if _, ok := r.Config["experimental"]; ok {
		t.Fatalf("experimental = %v", r.Config["experimental"])
	}
}

func TestEmitScripts(t *testing.T) {
	src := "$done({});"
	sum := sha256.Sum256([]byte(src))
	path := "http/" + hex.EncodeToString(sum[:]) + ".js"
	d := scriptDoc(
		&model.HTTPScript{ID: "script_1", Type: model.ScriptHTTPResponse, Pattern: `^https://a\.com/`, Source: src,
			RequiresBody: true, BinaryBody: true, MaxSize: 64 << 10, Timeout: 5, Argument: "k=v"},
		&model.HTTPScript{ID: "script_2", Tag: "run.once", Type: model.ScriptGeneric, Pattern: "ignored", Source: src, MaxSize: 4096, RequiresBody: true},
		&model.HTTPScript{ID: "script_3", Type: model.ScriptHTTPRequest, Pattern: "a", Source: src, Disabled: true},
	)
	r := emit.Emit(d, emit.Options{})
	want := []any{
		map[string]any{"tag": "script_1", "type": "http-response", "pattern": `^https://a\.com/`, "source_path": path, "source": src,
			"requires_body": true, "binary_body": true, "max_size": "64KiB", "timeout": "5s", "argument": "k=v"},
		map[string]any{"tag": "run.once", "type": "generic", "source_path": path, "source": src},
	}
	if !reflect.DeepEqual(r.Config["scripts"], want) {
		t.Fatalf("scripts = %#v", r.Config["scripts"])
	}
	cache := r.Config["experimental"].(map[string]any)["cache_file"].(map[string]any)
	if cache["enabled"] != true {
		t.Fatalf("cache_file = %v", cache)
	}
	if emit.ScriptSourcePath(src) != path {
		t.Fatal(emit.ScriptSourcePath(src))
	}
}

func TestEmitScriptsDisabledOnlyKeepsCacheFileOff(t *testing.T) {
	d := scriptDoc(&model.HTTPScript{ID: "s", Type: model.ScriptGeneric, Source: "x", Disabled: true})
	r := emit.Emit(d, emit.Options{})
	if _, ok := r.Config["scripts"]; ok {
		t.Fatal("disabled script emitted")
	}
	if _, ok := r.Config["experimental"]; ok {
		t.Fatal("cache file forced by a disabled script")
	}
}

func TestEmitScriptsSkipInvalidAndDuplicate(t *testing.T) {
	d := scriptDoc(
		&model.HTTPScript{ID: "bad1", Name: "坏", Type: model.ScriptHTTPRequest, Pattern: "(", Source: "x"},
		&model.HTTPScript{ID: "bad2", Type: "cron", Source: "x"},
		&model.HTTPScript{ID: "bad3", Type: model.ScriptGeneric, Source: " "},
		&model.HTTPScript{ID: "ok", Type: model.ScriptGeneric, Source: "x"},
		&model.HTTPScript{ID: "dup", Tag: "ok", Type: model.ScriptGeneric, Source: "y"},
	)
	r := emit.Emit(d, emit.Options{})
	items := r.Config["scripts"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["tag"] != "ok" {
		t.Fatalf("scripts = %v", items)
	}
	var joined []string
	for _, w := range r.Warnings {
		if strings.HasPrefix(w, "HTTPS 解密的脚本") {
			joined = append(joined, w)
		}
	}
	if len(joined) != 4 || !strings.Contains(joined[0], "「坏」") || !strings.Contains(joined[3], "重复") {
		t.Fatalf("warnings = %v", joined)
	}
}

func TestEmitScriptsIndependentOfMITM(t *testing.T) {
	d := scriptDoc(&model.HTTPScript{ID: "s", Type: model.ScriptGeneric, Source: "x"})
	d.HTTPTools.MITM = &model.MITMSettings{Enabled: true}
	r := emit.Emit(d, emit.Options{})
	if _, ok := r.Config["scripts"]; !ok {
		t.Fatal("no scripts")
	}
	if _, ok := r.Config["mitm"].(map[string]any)["scripts"]; ok {
		t.Fatal("scripts nested under mitm")
	}
}

func TestEmitCronScript(t *testing.T) {
	d := scriptDoc(
		&model.HTTPScript{ID: "tick", Type: model.ScriptCron, Cron: " */5 * * * * ", Pattern: "ignored", Source: "x", RequiresBody: true, MaxSize: 4096, Timeout: 30},
		&model.HTTPScript{ID: "nocron", Type: model.ScriptCron, Source: "x"},
		&model.HTTPScript{ID: "off", Type: model.ScriptCron, Cron: "@daily", Source: "x", Disabled: true},
	)
	r := emit.Emit(d, emit.Options{})
	want := []any{map[string]any{"tag": "tick", "type": "cron", "cron": "*/5 * * * *", "source_path": emit.ScriptSourcePath("x"), "source": "x", "timeout": "30s"}}
	if !reflect.DeepEqual(r.Config["scripts"], want) {
		t.Fatalf("scripts = %#v", r.Config["scripts"])
	}
}
