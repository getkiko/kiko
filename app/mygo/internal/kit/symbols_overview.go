package kit

// Filled symbols of the overview's power button, missing from the
// generated set: Lucide's square and play, filled with the text color.
const (
	// SymSquareFill is a filled square, "stop".
	SymSquareFill Symbol = "square-fill"
	// SymPlayFill is a filled triangle, "start".
	SymPlayFill Symbol = "play-fill"
)

func init() {
	symbolShapes[SymSquareFill] = `<rect width="18" height="18" x="3" y="3" rx="2" fill="currentColor" />`
	symbolShapes[SymPlayFill] = `<path d="M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z" fill="currentColor" />`
}

// Symbols of the policy group chooser's region rail, from lucide-static 1.63.0
// (ISC License): every region, and names without one.
const (
	// SymLayoutGrid is four squares, "all".
	SymLayoutGrid Symbol = "layout-grid"
	// SymCircleDashed is a dashed circle, "unrecognized".
	SymCircleDashed Symbol = "circle-dashed"
)

func init() {
	symbolShapes[SymLayoutGrid] = `<rect width="7" height="7" x="3" y="3" rx="1"/><rect width="7" height="7" x="14" y="3" rx="1"/><rect width="7" height="7" x="14" y="14" rx="1"/><rect width="7" height="7" x="3" y="14" rx="1"/>`
	symbolShapes[SymCircleDashed] = `<path d="M10.1 2.182a10 10 0 0 1 3.8 0"/><path d="M13.9 21.818a10 10 0 0 1-3.8 0"/><path d="M17.609 3.721a10 10 0 0 1 2.69 2.7"/><path d="M2.182 13.9a10 10 0 0 1 0-3.8"/><path d="M20.279 17.609a10 10 0 0 1-2.7 2.69"/><path d="M21.818 10.1a10 10 0 0 1 0 3.8"/><path d="M3.721 6.391a10 10 0 0 1 2.7-2.69"/><path d="M6.391 20.279a10 10 0 0 1-2.69-2.7"/>`
}
