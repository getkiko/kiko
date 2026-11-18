package kit

import "github.com/egoist/mygo/ui"

// MenuAnchor is the state of a menu that opens from a section heading's
// button or from one of the ＋ cards (AddCard) of the lists under it,
// and anchors to the one that opened it. Record the button and the cards
// with Button and Card as they are built, then build the menu at At
// after them, in the same pass.
type MenuAnchor struct {
	// Open tells that the menu is open.
	Open bool
	// Query is the search text of a menu that keeps its own, cleared
	// while the menu is closed.
	Query string
	// source is the card that opened the menu, "" for the button.
	source string
	button ui.Element
	cards  map[string]ui.Element
}

// Button records the heading's button el, whose click toggles the menu
// at it, and returns el.
func (m *MenuAnchor) Button(el ui.Element) ui.Element {
	m.button = el
	if el.Clicked() {
		m.Open, m.source = !m.Open, ""
	}
	return el
}

// Card records el, the ＋ card of source, whose click opens the menu at
// it, and returns el.
func (m *MenuAnchor) Card(source string, el ui.Element) ui.Element {
	if m.cards == nil {
		m.cards = map[string]ui.Element{}
	}
	m.cards[source] = el
	if el.Clicked() {
		m.Open, m.source = true, source
	}
	return el
}

// At is where to build the menu in this pass: the card that opened it,
// or the button when that card was not built, as when its list is
// filtered out. source is the card's, "" for the button; ok is false
// when neither was built. A closed menu forgets its card and Query.
func (m *MenuAnchor) At() (anchor ui.Element, source string, ok bool) {
	if !m.Open {
		m.source, m.Query = "", ""
	}
	if card := m.cards[m.source]; m.source != "" && card.Valid() {
		return card, m.source, true
	}
	return m.button, "", m.button.Valid()
}
