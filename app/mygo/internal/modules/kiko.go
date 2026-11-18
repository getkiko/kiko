package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Kiko module files (*.kikomodule) are JSON: a format marker, a format
// version and the converted module. Scripts carry their source. Files
// exported before the app was renamed carry the marker poppymodule and
// read the same.
const (
	KikoFormat        = "kikomodule"
	legacyFormat      = "poppymodule"
	KikoFormatVersion = 1
	// MaxKikoSize bounds a file: scripts and mapping bodies up to 4 MiB
	// plus JSON overhead.
	MaxKikoSize = 16 << 20
)

type kikoFile struct {
	Format        string       `json:"format"`
	FormatVersion int          `json:"formatVersion"`
	Name          string       `json:"name"`
	Description   string       `json:"description,omitempty"`
	Version       string       `json:"version,omitempty"`
	Contents      kikoContents `json:"contents"`
	Unsupported   []kikoUnsupp `json:"unsupported,omitempty"`
}

type kikoContents = model.ModuleContents

type kikoUnsupp = model.UnsupportedItem

// ParseKiko reads a .kikomodule file. The file is validated strictly:
// unknown fields, a wrong format or version, oversized or invalid content
// fail with an error.
func ParseKiko(data []byte) (*Result, error) {
	if len(data) > MaxKikoSize {
		return nil, errors.New("模块文件过大")
	}
	var f kikoFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, errors.New("模块文件无法解析")
	}
	if dec.More() {
		return nil, errors.New("模块文件含有多余内容")
	}
	if f.Format != KikoFormat && f.Format != legacyFormat {
		return nil, ErrNotModule
	}
	if f.FormatVersion != KikoFormatVersion {
		return nil, errors.New("不支持的模块文件版本 " + strconv.Itoa(f.FormatVersion))
	}
	if len(f.Unsupported) > model.MaxModuleUnsupported {
		return nil, errors.New("模块文件的未支持项过多")
	}
	m := &model.HTTPModule{ID: model.NewID("mod"), Name: f.Name, Description: f.Description, Version: f.Version,
		Source: model.ModuleSource{Kind: model.ModuleSourceFile}, Contents: f.Contents}
	if issue := m.Issue(); issue != "" {
		return nil, errors.New(issue)
	}
	return &Result{Name: f.Name, Description: f.Description, Version: f.Version, Contents: f.Contents, Unsupported: f.Unsupported}, nil
}

// ExportKiko writes the module as a .kikomodule file: content and script
// sources, without the enabled state, confirmations, source address and run
// state.
func ExportKiko(m *model.HTTPModule) []byte {
	f := kikoFile{Format: KikoFormat, FormatVersion: KikoFormatVersion, Name: m.Name, Description: m.Description,
		Version: m.Version, Contents: m.Contents, Unsupported: m.Unsupported}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil
	}
	return append(out, '\n')
}
