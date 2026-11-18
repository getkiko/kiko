package logs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TextStyle is the style of a run of terminal text: colors are CSS
// strings ("#rrggbb", "rgb(r, g, b)").
type TextStyle struct {
	Color      string
	Background string
	Weight     int
	Italic     bool
	Decoration string // "underline" or "line-through"
	Opacity    float64
}

// Segment is a run of a terminal line with one style.
type Segment struct {
	Text  string
	Style TextStyle
}

var ansiColors = [16]string{
	"#1f2937", "#ef4444", "#22c55e", "#eab308",
	"#3b82f6", "#d946ef", "#2dd4bf", "#d1d5db",
	"#6b7280", "#f87171", "#4ade80", "#facc15",
	"#60a5fa", "#e879f9", "#5eead4", "#f9fafb",
}

var levelColors = map[string]string{
	"TRACE": "#d1d5db", "DEBUG": "#d1d5db", "INFO": "#2dd4bf", "WARN": "#facc15", "WARNING": "#facc15",
	"ERROR": "#f87171", "FATAL": "#f87171", "PANIC": "#f87171",
}

var (
	csiPattern   = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	levelPattern = regexp.MustCompile(`(?i)\b(TRACE|DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL|PANIC)\b`)
)

func color256(index int) string {
	switch {
	case index < 0 || index > 255:
		return ""
	case index < 16:
		return ansiColors[index]
	case index < 232:
		v := index - 16
		values := []int{0, 95, 135, 175, 215, 255}
		return fmt.Sprintf("rgb(%d, %d, %d)", values[v/36], values[v/6%6], values[v%6])
	}
	gray := 8 + (index-232)*10
	return fmt.Sprintf("rgb(%d, %d, %d)", gray, gray, gray)
}

func extendedColor(params []int, i int, set func(string)) int {
	if i+2 < len(params) && params[i+1] == 5 {
		if c := color256(params[i+2]); c != "" {
			set(c)
		}
		return i + 2
	}
	if i+4 < len(params) && params[i+1] == 2 {
		rgb := params[i+2 : i+5]
		clamp := func(v int) int { return min(255, max(0, v)) }
		set(fmt.Sprintf("rgb(%d, %d, %d)", clamp(rgb[0]), clamp(rgb[1]), clamp(rgb[2])))
		return i + 4
	}
	return i
}

func applySGR(sequence string, s *TextStyle) {
	body := sequence[2 : len(sequence)-1]
	params := []int{0}
	if body != "" {
		params = params[:0]
		for _, p := range strings.Split(body, ";") {
			n, _ := strconv.Atoi(p)
			params = append(params, n)
		}
	}
	for i := 0; i < len(params); i++ {
		code := params[i]
		switch {
		case code == 0:
			*s = TextStyle{}
		case code == 1:
			s.Weight = 700
		case code == 2:
			s.Opacity = 0.65
		case code == 3:
			s.Italic = true
		case code == 4:
			s.Decoration = "underline"
		case code == 9:
			s.Decoration = "line-through"
		case code == 22:
			s.Weight, s.Opacity = 0, 0
		case code == 23:
			s.Italic = false
		case code == 24 || code == 29:
			s.Decoration = ""
		case code >= 30 && code <= 37:
			s.Color = ansiColors[code-30]
		case code == 38:
			i = extendedColor(params, i, func(c string) { s.Color = c })
		case code == 39:
			s.Color = ""
		case code >= 40 && code <= 47:
			s.Background = ansiColors[code-40]
		case code == 48:
			i = extendedColor(params, i, func(c string) { s.Background = c })
		case code == 49:
			s.Background = ""
		case code >= 90 && code <= 97:
			s.Color = ansiColors[code-90+8]
		case code >= 100 && code <= 107:
			s.Background = ansiColors[code-100+8]
		}
	}
}

func pushSegment(segments []Segment, text string, style TextStyle) []Segment {
	if text == "" {
		return segments
	}
	if n := len(segments); n > 0 && segments[n-1].Style == style {
		segments[n-1].Text += text
		return segments
	}
	return append(segments, Segment{Text: text, Style: style})
}

// ParseLine converts a terminal line's SGR escape sequences into styled
// segments, dropping other control sequences. A line without colors gets
// its log level colored.
func ParseLine(line string) []Segment {
	var segments []Segment
	var style TextStyle
	cursor := 0
	for _, m := range csiPattern.FindAllStringIndex(line, -1) {
		segments = pushSegment(segments, line[cursor:m[0]], style)
		if seq := line[m[0]:m[1]]; strings.HasSuffix(seq, "m") {
			applySGR(seq, &style)
		}
		cursor = m[1]
	}
	segments = pushSegment(segments, line[cursor:], style)
	if len(segments) == 0 {
		segments = []Segment{{}}
	}
	return levelColor(segments)
}

func levelColor(segments []Segment) []Segment {
	var text strings.Builder
	for _, s := range segments {
		if s.Style.Color != "" {
			return segments
		}
		text.WriteString(s.Text)
	}
	all := text.String()
	m := levelPattern.FindStringIndex(all)
	if m == nil {
		return segments
	}
	level := all[m[0]:m[1]]
	var out []Segment
	if m[0] > 0 {
		out = append(out, Segment{Text: all[:m[0]]})
	}
	out = append(out, Segment{Text: level, Style: TextStyle{Color: levelColors[strings.ToUpper(level)], Weight: 600}})
	if m[1] < len(all) {
		out = append(out, Segment{Text: all[m[1]:]})
	}
	return out
}
