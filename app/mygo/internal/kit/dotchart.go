package kit

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// ChartTone is a dot chart's color.
type ChartTone int

const (
	ChartBlue ChartTone = iota
	ChartGreen
	ChartOrange
	ChartPink
)

func (t ChartTone) color(k *Tokens) ui.Color {
	switch t {
	case ChartGreen:
		return ui.Hex("#30B34C")
	case ChartOrange:
		return k.Warning
	case ChartPink:
		return ui.Hex("#DF668A")
	}
	return ui.Hex("#0A84FF")
}

// DotChartOptions describe a dot chart.
type DotChartOptions struct {
	Label string
	// Value is the current value, shown beside the label.
	Value string
	Tone  ChartTone
	// MinimumScale is the least full-height value, 1 when 0.
	MinimumScale float64
	// Columns is how many samples show, 44 when 0.
	Columns int
	// Description explains the metric under the chart.
	Description string
	// SampleLabels name the samples, as their times, for the tooltip.
	SampleLabels []string
	// Bare draws only the dots, without the tinted card and its header,
	// for a trend inside a page's own card (the Overview metric cards).
	Bare bool
	// Height is the dots' height, 34 when 0.
	Height float32
}

// DotChart is NativeDotChart: the latest samples as columns of six dots,
// filled up to each sample's share of the largest. A nil sample is a gap
// that keeps its place in time.
func DotChart(c *ui.Context, values []*float64, o DotChartOptions) ui.Element {
	k := T(c)
	color := o.Tone.color(k)
	columns := o.Columns
	if columns <= 0 {
		columns = 44
	}
	samples := values
	if len(samples) > columns {
		samples = samples[len(samples)-columns:]
	}
	scale := math.Max(o.MinimumScale, 1)
	if o.MinimumScale > 0 {
		scale = o.MinimumScale
	}
	for _, v := range samples {
		if v != nil {
			scale = math.Max(scale, *v)
		}
	}
	pad := columns - len(samples)
	labels := o.SampleLabels
	if len(labels) > len(samples) {
		labels = labels[len(labels)-len(samples):]
	}
	height := o.Height
	if height <= 0 {
		height = 34
	}
	card := ui.Column(c).MinWidth(0).Label(o.Label)
	if !o.Bare {
		card.Padding(12).Radius(13).Background(color.Mix(k.Surface2, 0.92))
	}
	card.Children(func() {
		if !o.Bare {
			ui.Row(c).Gap(8).Margin(0, 0, 12).Children(func() {
				ui.Text(c, o.Label).FontSize(FontTable).FontWeight(600).TextColor(color).Grow(1).SingleLine()
				ui.Text(c, o.Value).FontSize(FontTable).FontWeight(600).TextColor(k.Label).FontFeatures("tnum").SingleLine()
			})
		}
		matrix := ui.Box(c).Height(height).FillWidth()
		if x, _, inside := matrix.PointerPosition(); inside && len(labels) > 0 {
			w := matrix.Bounds().W
			column := int(x / w * float32(columns))
			if i := column - pad; i >= 0 && i < len(labels) && labels[i] != "" {
				Hint(c, matrix, labels[i])
			}
		}
		matrix.Draw(func(p *ui.Painter, r ui.Rect) {
			const gap = 2
			cw := (r.W - gap*float32(columns-1)) / float32(columns)
			dh := (r.H - gap*5) / 6
			d := min(cw, dh, 4)
			for col := range columns {
				var sample *float64
				if i := col - pad; i >= 0 {
					sample = samples[i]
				}
				cx := r.X + float32(col)*(cw+gap) + cw/2
				for row := range 6 {
					filled := false
					if sample != nil {
						share := *sample / scale
						if *sample > 0 {
							share = math.Max(share, 1.0/6)
						}
						filled = float64(6-row)/6 <= share
					}
					alpha := float32(0.17)
					if filled {
						alpha = 1
					}
					cy := r.Y + float32(row)*(dh+gap) + dh/2
					p.Fill(ui.Rect{X: cx - d/2, Y: cy - d/2, W: d, H: d}, color.Alpha(alpha), d/2)
				}
			}
		})
		if o.Description != "" {
			ui.Text(c, o.Description).FontSize(10).TextColor(k.Label3).Margin(8, 0, 0)
		}
	})
	return card
}

// Sample returns a pointer to v, for a chart's values.
func Sample(v float64) *float64 { return &v }
