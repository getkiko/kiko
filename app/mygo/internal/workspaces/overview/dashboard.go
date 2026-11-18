package overview

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// metricsState is the core's metric history (OverviewMetrics).
type metricsState struct {
	points []api.MetricPoint
	err    string
	// width is the dashboard grid's width in the last frame.
	width float32
	// peaks are the largest values seen by metric during the core run
	// started at peaksRun.
	peaks    map[string]float64
	peaksRun string
	// advance counts the samples the polls have appended, which the
	// trends slide by.
	advance int
	// trends keep the trends' motion: memory, connections, upload and
	// download.
	trends [4]kit.AreaChartState
}

// trendGlide is how long a trend takes to glide in a new sample.
const trendGlide = 600 * time.Millisecond

// metricsPoll is how often the metric history is read: as often as the
// server samples, so each sample arrives on its own and the figures and
// trends move once a second together.
const metricsPoll = time.Second

// appended counts the samples of next after the latest of prev.
func appended(prev, next []api.MetricPoint) int {
	if len(prev) == 0 {
		return 0
	}
	latest, count := prev[len(prev)-1].Time, 0
	for _, p := range next {
		if p.Time > latest {
			count++
		}
	}
	return count
}

// pollMetrics reads GET /metrics/history every metricsPoll.
func (w *Workspace) pollMetrics(ctx context.Context) {
	for {
		points, err := w.env.API.MetricsHistory(ctx)
		if ctx.Err() != nil {
			return
		}
		w.env.Post(func() {
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				w.metrics.err = err.Error()
				return
			}
			w.metrics.advance += appended(w.metrics.points, points)
			w.metrics.points, w.metrics.err = points, ""
		})
		if sleep(ctx, metricsPoll) != nil {
			return
		}
	}
}

// metricColumns is the dashboard grid's columns for its width: four on
// wide windows (the state and the mode spanning two), two, or one on
// narrow ones.
func metricColumns(width float32) int {
	switch {
	case width <= 0 || width >= 880:
		return 4
	case width >= 420:
		return 2
	}
	return 1
}

// dashboardCard is a card of the Overview dashboard: white surface, rounded,
// generous padding, spanning span columns of its grid; its surface is
// kit.CardSurface, as the Components page's hero cards.
func dashboardCard(c *ui.Context, label string, span int) ui.Element {
	card := kit.CardSurface(c, ui.Column(c).MinWidth(0).Padding(16, 18).Label(label), 20)
	if span > 1 {
		card.ColumnSpan(span)
	}
	return card
}

// cardHeader is a card's title row: symbol, title, and trailing content
// at the right.
func cardHeader(c *ui.Context, icon kit.Symbol, iconColor ui.Color, title string, trailing func()) {
	k := kit.T(c)
	ui.Row(c).Gap(8).MinHeight(22).AlignItems(ui.Center).Children(func() {
		kit.Icon(c, icon, 16).TextColor(iconColor)
		ui.Text(c, title).FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label).SingleLine()
		ui.Spacer(c)
		if trailing != nil {
			trailing()
		}
	})
}

// splitUnit separates a formatted value ("1.5 KB/s") into its number and
// unit.
func splitUnit(value string) (string, string) {
	if i := strings.LastIndexByte(value, ' '); i >= 0 {
		return value[:i], value[i+1:]
	}
	return value, ""
}

// dashboard is the grid of the core's state, upload and download, the
// outbound mode, memory and active connections, then the sampling note.
func (w *Workspace) dashboard(c *ui.Context) {
	k := kit.T(c)
	server := w.env.Server
	runtime, known := server.Runtime, server.HasRuntime
	columns := metricColumns(w.metrics.width)
	span := min(2, columns)

	points := w.metrics.points
	var latest *api.MetricPoint
	if len(points) > 0 {
		latest = &points[len(points)-1]
	}
	available := latest != nil && latest.Upload != nil
	if known {
		available = runtime.Running() && runtime.MetricsAvailable
	}
	// value is the latest sample's, the one the trend brings in at its
	// right edge, so the figure and the chart change together; the
	// runtime's current value before any sample.
	value := func(current *float64, sample func(api.MetricPoint) *float64) *float64 {
		if !available {
			return nil
		}
		if latest != nil {
			if v := sample(*latest); v != nil {
				return v
			}
		}
		if known && current != nil {
			return current
		}
		return kit.Sample(0)
	}
	number := func(v *float64, rate bool) string {
		if v == nil {
			return "—"
		}
		if rate {
			return usageBytes(*v) + "/s"
		}
		return usageBytes(*v)
	}
	total := func(current *float64, sample func(api.MetricPoint) string) string {
		if !available {
			return "—"
		}
		if known && current != nil {
			return usageBytes(*current)
		}
		if latest != nil {
			return usageBytes(parseTotal(sample(*latest)))
		}
		return usageBytes(0)
	}
	series := func(sample func(api.MetricPoint) *float64) []*float64 {
		out := make([]*float64, len(points))
		for i, p := range points {
			out[i] = sample(p)
		}
		return out
	}
	// peak is the largest value of a metric recorded during this core
	// run: the samples kept, the current value, and what earlier frames
	// saw before the history dropped it.
	if m := &w.metrics; m.peaks == nil || m.peaksRun != runtime.StartedAt {
		m.peaks, m.peaksRun = map[string]float64{}, runtime.StartedAt
	}
	peak := func(key string, current *float64, sample func(api.MetricPoint) *float64) *float64 {
		best, seen := w.metrics.peaks[key]
		consider := func(v *float64) {
			if v != nil && !math.IsNaN(*v) && (!seen || *v > best) {
				best, seen = *v, true
			}
		}
		for _, p := range points {
			consider(sample(p))
		}
		consider(current)
		if !seen {
			return nil
		}
		w.metrics.peaks[key] = best
		return &best
	}
	upload := func(p api.MetricPoint) *float64 { return p.Upload }
	download := func(p api.MetricPoint) *float64 { return p.Download }
	memory := func(p api.MetricPoint) *float64 { return p.Memory }
	connections := func(p api.MetricPoint) *float64 { return p.Connections }
	uploadTotal := func(p api.MetricPoint) string { return p.UploadTotal }
	downloadTotal := func(p api.MetricPoint) string { return p.DownloadTotal }
	rate := func(v float64) string { return usageBytes(v) + "/s" }
	count := func(v *float64) string {
		if v == nil {
			return "—"
		}
		return strconv.FormatFloat(*v, 'f', -1, 64)
	}

	// FlClash's series colors: the accent, and the accent at 45% over
	// the card.
	series2 := k.Surface.Mix(k.Accent, 0.45)

	ui.Column(c).Gap(10).Shrink(0).Children(func() {
		ui.Grid(c.Key("metrics")).Columns(columns).Gap(12).Label("核心运行指标").Children(func() {
			w.statusCard(c, span, runtime, known)
			memoryNow := value(runtime.MemoryBytes, memory)
			metricCard(c, metricView{
				label: "内存", icon: kit.SymCpu, iconColor: k.Label2,
				value: number(memoryNow, false), peak: number(peak("memory", memoryNow, memory), false),
				values: series(memory), color: series2, format: usageBytes, floor: memoryFloor, advance: w.metrics.advance, trend: &w.metrics.trends[0], live: available,
			})
			connectionsNow := value(runtime.ActiveConnections, connections)
			metricCard(c, metricView{
				label: "活动连接", icon: kit.SymNetwork, iconColor: k.Label2,
				value: count(connectionsNow), peak: count(peak("connections", connectionsNow, connections)),
				values: series(connections), color: k.Accent, floor: connectionsFloor, advance: w.metrics.advance, trend: &w.metrics.trends[1], live: available,
				format: func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) },
			})
			// The mode over the profile, as tall together as the upload
			// and download cards beside them, whose trends fill the room.
			column := ui.Column(c).Gap(12).MinWidth(0)
			if span > 1 {
				column.ColumnSpan(span)
			}
			column.Children(func() {
				w.modeCard(c, 1)
				w.profileCard(c)
			})
			uploadNow := value(runtime.UploadRate, upload)
			downloadNow := value(runtime.DownloadRate, download)
			metricCard(c, metricView{
				label: "上传", icon: kit.SymArrowUp, iconColor: k.Label2,
				value: number(uploadNow, true), peak: number(peak("upload", uploadNow, upload), true),
				detail: "累计 " + total(runtime.UploadTotal, uploadTotal),
				values: series(upload), color: series2, format: rate, floor: rateFloor, advance: w.metrics.advance, trend: &w.metrics.trends[2], live: available,
			})
			metricCard(c, metricView{
				label: "下载", icon: kit.SymArrowDown, iconColor: k.Accent,
				value: number(downloadNow, true), peak: number(peak("download", downloadNow, download), true),
				detail: "累计 " + total(runtime.DownloadTotal, downloadTotal),
				values: series(download), color: k.Accent, format: rate, floor: rateFloor, advance: w.metrics.advance, trend: &w.metrics.trends[3], live: available,
			})
		})
		if w.metrics.err != "" {
			ui.Text(c, "趋势读取失败："+w.metrics.err).FontSize(kit.FontCaption).TextColor(k.Danger).PaddingX(4)
		}
	})
}

// statusCard is the core's state: on the left a chip with the state,
// the server the window shows (its name and address, what the toolbar
// once carried) and its uptime; on the right the power control, which
// unfolds a reload button while the core runs.
func (w *Workspace) statusCard(c *ui.Context, span int, runtime api.Runtime, known bool) {
	k := kit.T(c)
	running := known && runtime.Running()
	dashboardCard(c, "核心状态", span).Children(func() {
		ui.Row(c).Gap(16).Grow(1).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(10).Children(func() {
				ui.Row(c).Gap(8).MinHeight(26).AlignItems(ui.Center).Children(func() {
					kit.Icon(c, kit.SymActivity, 16).TextColor(k.Label2)
					ui.Text(c, "状态").FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label).SingleLine()
					statusChip(c, runtime, known)
				})
				w.serverStatus(c)
				if runtime.Error != "" {
					ui.Text(c, runtime.Error).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Ellipsis("…")
				}
				ui.Spacer(c)
				ui.Row(c).Gap(32).AlignItems(ui.Start).Children(func() {
					uptime := "—"
					if running {
						uptime = kit.Uptime(runtime.UptimeSeconds)
					}
					statusField(c, "运行时长", uptime)
				})
			})
			w.powerControl(c, runtime, known)
		})
	})
}

// statusChip is the state as a capsule: a dot (green running, orange on
// the way, gray stopped) and the state's words.
func statusChip(c *ui.Context, runtime api.Runtime, known bool) {
	k := kit.T(c)
	dot, fill := k.Label4, k.Fill
	switch {
	case !known, runtime.State == "stopped", runtime.State == "":
	case runtime.Running():
		dot, fill = k.Success, k.SuccessWeak
	default:
		dot, fill = k.Warning, k.WarningWeak
	}
	status := statusText(runtime, known)
	chip := ui.Row(c).Gap(6).Height(22).PaddingX(9).Radius(kit.RadiusCapsule).Background(fill).
		AlignItems(ui.Center).MinWidth(0).Shrink(1).Label(status)
	if runtime.Error != "" {
		kit.Hint(c, chip, runtime.Error)
	}
	chip.Children(func() {
		ui.Box(c).Size(7, 7).Radius(3.5).Background(dot).Shrink(0)
		ui.Text(c, status).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label2).SingleLine().Ellipsis("…")
	})
}

// statusField is a caption over a value, in the state card.
func statusField(c *ui.Context, label, value string) {
	k := kit.T(c)
	ui.Column(c).Gap(3).MinWidth(0).Label(label).Children(func() {
		ui.Text(c, label).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine()
		ui.Text(c, value).FontSize(15).FontWeight(600).TextColor(k.Label).FontFeatures("tnum").SingleLine()
	})
}

// metricColumnsShown is how many of the latest samples a metric's trend
// spans, two thirds of the history kept, so its peaks spread wider.
const metricColumnsShown = 30

// The least values the trends' tops stand for, so idle values keep near
// the bottom: FlClash's 8 KB/s for traffic.
const (
	rateFloor        = 8 << 10
	memoryFloor      = 16 << 20
	connectionsFloor = 5
)

// metricView is what a metric card shows.
type metricView struct {
	label     string
	icon      kit.Symbol
	iconColor ui.Color
	// value and peak are formatted with their unit ("1.5 KB/s").
	value, peak string
	detail      string
	values      []*float64
	color       ui.Color
	// format formats a sample, for the chart's readout.
	format func(float64) string
	// floor is the least value the chart's top stands for.
	floor float64
	// advance counts the samples appended, for the trend to slide by,
	// and trend keeps its motion; live draws the trend, while the core
	// runs.
	advance int
	trend   *kit.AreaChartState
	live    bool
}

// metricCard is a metric's card: title and peak, the current value large
// with a smaller unit, a detail, and its trend as a smooth area chart
// along the card's bottom edge.
func metricCard(c *ui.Context, m metricView) {
	k := kit.T(c)
	number, unit := splitUnit(m.value)
	dashboardCard(c, m.label, 1).Padding(0).Clip().Children(func() {
		ui.Column(c).Padding(16, 18, 0).Gap(6).MinWidth(0).Shrink(0).Children(func() {
			cardHeader(c, m.icon, m.iconColor, m.label, func() {
				ui.Text(c, "峰值 "+m.peak).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").SingleLine()
			})
			ui.Row(c).Gap(4).Margin(6, 0, 0).AlignItems(ui.Baseline).Label(m.label + " " + m.value).Children(func() {
				ui.Text(c, number).FontSize(32).FontWeight(600).TextColor(k.Label).FontFeatures("tnum").SingleLine()
				if unit != "" {
					ui.Text(c, unit).FontSize(14).FontWeight(500).TextColor(k.Label2).SingleLine()
				}
			})
			if m.detail != "" {
				ui.Text(c, m.detail).FontSize(kit.FontTable).TextColor(k.Label3).FontFeatures("tnum").SingleLine()
			}
		})
		// The trend takes the card's room below the text, as the cards of
		// a row stretch to the tallest; the room stays empty, and nothing
		// moves, while the core is stopped.
		if !m.live {
			if m.trend != nil {
				*m.trend = kit.AreaChartState{} // starts at rest when it shows again
			}
			ui.Box(c).FillWidth().Grow(1).MinHeight(52).Margin(8, 0, 0)
			return
		}
		kit.AreaChart(c, m.values, kit.AreaChartOptions{
			Label: m.label + "趋势", Color: m.color, Height: 52, Fill: true, Format: m.format,
			Columns: metricColumnsShown, MinimumScale: m.floor,
			Advance: m.advance, AdvanceDuration: trendGlide, State: m.trend,
		}).Margin(8, 0, 0)
	})
}

// parseTotal reads a decimal byte count, 0 when it is not one.
func parseTotal(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) {
		return 0
	}
	return v
}
