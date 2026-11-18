package kit

import (
	"math"

	"github.com/egoist/mygo/ui"
)

const (
	// QRQuietZone is the light margin around a QR code, in modules.
	QRQuietZone = 4
	// QRSize is the side of a QR tile in DIPs.
	QRSize = 176
	// qrMaxModules is the side of the largest QR code (version 40).
	qrMaxModules = 177
	qrMinModules = 21
)

// QRMatrix is a QR code's modules, row by row; true is a dark module.
type QRMatrix struct {
	Size  int
	Cells [][]bool
}

// ParseQR reads rows of '1' (dark) and '0' (light) as a square QR code.
// It reports false for a matrix that cannot be a QR code.
func ParseQR(rows []string) (QRMatrix, bool) {
	n := len(rows)
	if n < qrMinModules || n > qrMaxModules {
		return QRMatrix{}, false
	}
	cells := make([][]bool, n)
	for y, row := range rows {
		if len(row) != n {
			return QRMatrix{}, false
		}
		cells[y] = make([]bool, n)
		for x := range len(row) {
			switch row[x] {
			case '1':
				cells[y][x] = true
			case '0':
			default:
				return QRMatrix{}, false
			}
		}
	}
	return QRMatrix{Size: n, Cells: cells}, true
}

// QRModuleSize is the side of one module in DIPs for a tile of size DIPs:
// a whole number, so the edges stay crisp at whole display scales. At
// least one DIP.
func QRModuleSize(size float32, modules int) float32 {
	return max(float32(math.Floor(float64(size/float32(modules+2*QRQuietZone)))), 1)
}

// QRCode draws m as dark modules on a light tile with the quiet zone. The
// tile stays white in every theme, as scanners need dark on light.
func QRCode(c *ui.Context, m QRMatrix, label string) ui.Element {
	module := QRModuleSize(QRSize, m.Size)
	side := module * float32(m.Size+2*QRQuietZone)
	tile := ui.Box(c).Width(side).Height(side).Shrink(0).Radius(8).Background(ui.Hex("#FFFFFF"))
	if label != "" {
		tile.Label(label)
	}
	tile.Draw(func(p *ui.Painter, r ui.Rect) {
		dark := ui.Hex("#000000")
		for y, row := range m.Cells {
			for x := 0; x < len(row); x++ {
				if !row[x] {
					continue
				}
				// A run of dark modules is one rectangle.
				end := x
				for end+1 < len(row) && row[end+1] {
					end++
				}
				p.Fill(ui.Rect{X: r.X + float32(x+QRQuietZone)*module, Y: r.Y + float32(y+QRQuietZone)*module,
					W: float32(end-x+1) * module, H: module}, dark, 0)
				x = end
			}
		}
	})
	return tile
}
