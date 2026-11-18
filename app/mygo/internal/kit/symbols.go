package kit

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// Symbol names a semantic icon of the console, NativeSymbols' counterpart.
// The shapes are Lucide's, drawn in the color of the text around them.
type Symbol string

var (
	symbolMu    sync.Mutex
	symbolCache = map[Symbol]*ui.SVG{}
)

// SVG returns the parsed icon, parsing it once.
func (s Symbol) SVG() *ui.SVG {
	symbolMu.Lock()
	defer symbolMu.Unlock()
	if svg, ok := symbolCache[s]; ok {
		return svg
	}
	shapes, ok := symbolShapes[s]
	if !ok {
		shapes = symbolShapes[SymCircle]
	}
	svg := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
	symbolCache[s] = svg
	return svg
}

// Icon shows a symbol at size DIPs in the current text color.
func Icon(c *ui.Context, s Symbol, size float32) ui.Element {
	return ui.Icon(c, s.SVG()).FontSize(size).Size(size, size).Shrink(0)
}
