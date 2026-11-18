package model

import (
	"fmt"
	"strconv"
	"strings"
)

// The scanner below is a faithful copy of the core's script/complexity.go
// (CheckSource with ScriptLimits). The model is its spec copy: a script the
// core refuses is never accepted here. Names carry a prefix to stay apart
// from the rest of the package; keep both files in step.

// scanError joins its parts like the core's error constructor.
func scanError(parts ...any) error {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(fmt.Sprint(p))
	}
	return fmt.Errorf("脚本：%s", b.String())
}

// scriptLimits bound the shape of script source before it is parsed. The parser and compiler
// of goja recurse per nesting level (a deep source overflows the Go stack, which kills the
// process) and some constructs compile in quadratic time (a chain of 50000 "1+" takes
// seconds), so sources are measured first.
type scriptLimits struct {
	// Length is the longest source in bytes.
	Length int
	// Depth is the deepest bracket nesting.
	Depth int
	// Chain is the most operators (and call, index and template openings) in one operand
	// sequence: between commas, semicolons and braces at one bracket level.
	Chain int
	// Openers is the most "(", "[" and "{" bytes anywhere in the source, strings, comments
	// and regular expressions included. The nesting depth of any reading of the source is at
	// most this, so it bounds the parser's stack even if the scan misreads the source.
	Openers int
	// Operators is the most operator bytes anywhere in the source, counted the same way. It
	// bounds the compile time of a chain the scan did not see.
	Operators int
}

var scriptSourceLimits = scriptLimits{Length: MaxScriptSource, Depth: MaxScriptDepth, Chain: 5000, Openers: 100000, Operators: 500000}

// scanExprWords are the keywords after which an expression follows, so a "/" starts a regular
// expression.
var scanExprWords = map[string]bool{
	"typeof": true, "void": true, "new": true, "delete": true,
	"in": true, "instanceof": true, "return": true, "case": true, "throw": true,
}

// scanControlWords are followed by a parenthesised head and then a statement.
var scanControlWords = map[string]bool{"if": true, "while": true, "for": true, "with": true, "catch": true, "switch": true}

// scanBlockWords are followed by a statement or block.
var scanBlockWords = map[string]bool{"else": true, "do": true, "try": true, "finally": true}

// scanSoftWords can be a keyword or a name: a "/" after them may be either.
var scanSoftWords = map[string]bool{"of": true, "yield": true, "await": true}

// token classes remembered between tokens; they decide what a "/" and a "{" mean.
const (
	scanTokStart    byte = 'S' // statement start: file start, ";", "{", the "}" of a block
	scanTokExpr     byte = 'E' // an expression is expected: operator, "(", "[", ",", keyword
	scanTokColon    byte = ':' // a label, a case, a property or the else of a conditional
	scanTokArrow    byte = 'A' // "=>"
	scanTokControl  byte = 'C' // the ")" of an if, while, for, with, catch or switch head
	scanTokFnExpr   byte = 'F' // the ")" of the parameters of a function expression
	scanTokFnDecl   byte = 'D' // the ")" of the parameters of a function declaration
	scanTokParen    byte = 'P' // any other ")"
	scanTokOperand  byte = 'V' // identifier, number, string, regular expression, "]", the "}" of an expression
	scanTokBlockKw  byte = 'B' // else, do, try, finally
	scanTokAmbigous byte = 'X' // a soft keyword, or a "}" whose kind is unknown
)

// scanSlashAfter reports how a "/" reads after a token of class prev.
func scanSlashAfter(prev byte) scanSlashMode {
	switch prev {
	case scanTokOperand, scanTokFnExpr, scanTokFnDecl, scanTokParen:
		return scanSlashDivision
	case scanTokAmbigous:
		return scanSlashAmbiguous
	}
	return scanSlashRegex
}

// scanSlashMode is what a "/" means at this point of the source.
type scanSlashMode uint8

const (
	scanSlashDivision  scanSlashMode = iota // after an operand
	scanSlashRegex                          // after an operator, "(" or a keyword
	scanSlashAmbiguous                      // after a soft keyword or a "}" of unknown kind
)

type scanLevel struct {
	kind  byte // 0 base, '(' '[' '{', '$' for a ${ } of a template literal
	chain int
	// fn is set on a "(" : scanTokControl, scanTokFnExpr or scanTokFnDecl for the heads that decide what
	// follows the ")".
	fn byte
	// after is the class a "}" gets when it closes this "{".
	after byte
}

// scriptScanState is one reading of the source. The meaning of a "/" is decided by the tokens
// before it; where they leave it open, the scan follows both readings.
type scriptScanState struct {
	pos       int
	prev      byte
	prevDot   bool
	pendingFn byte // scanTokFnExpr or scanTokFnDecl between the function keyword and its "("
	control   bool // the previous word was if, while, for, with, catch or switch
	classHdr  bool // between the class keyword and its "{"
	classStmt bool // that class is a declaration
	asyncStmt bool // the async keyword stood at a statement start
	wasAsync  bool // the previous word was async
	levels    []scanLevel
}

func (s *scriptScanState) clone() *scriptScanState {
	c := *s
	c.levels = append(make([]scanLevel, 0, len(s.levels)+8), s.levels...)
	return &c
}

func (s *scriptScanState) key() string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(s.pos))
	b.WriteByte(s.prev)
	b.WriteByte(s.pendingFn)
	for _, v := range []bool{s.prevDot, s.control, s.classHdr, s.classStmt, s.asyncStmt, s.wasAsync} {
		if v {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	for _, lv := range s.levels {
		b.WriteByte(lv.kind)
		b.WriteByte(lv.fn)
		b.WriteByte(lv.after)
		b.WriteString(strconv.Itoa(lv.chain))
		b.WriteByte(',')
	}
	return b.String()
}

// checkScriptSource reports why src is too large or too deeply built to be parsed safely. It is a
// scan, not a parse: strings, comments, templates and regular expression literals are
// skipped, brackets are matched, and operators are counted. The tokens before a "/" decide
// whether it divides or starts a regular expression; where they leave it open, every reading
// is followed and the worst one counts. A reading that does not form a program (unmatched
// brackets, an unterminated string or regular expression) is dropped, and a source with no
// reading left is refused. scriptRawGuard backs the scan with totals that do not depend on how the
// source reads.
func checkScriptSource(src string, l scriptLimits) error {
	if len(src) > l.Length {
		return scanError("source larger than ", l.Length, " bytes")
	}
	if err := scriptRawGuard(src, l); err != nil {
		return err
	}
	budget := 16*len(src) + 4096
	first := &scriptScanState{levels: []scanLevel{{}}, prev: scanTokStart}
	work := []*scriptScanState{first}
	seen := map[string]bool{first.key(): true}
	finished := false
	var firstDeath error
	for len(work) > 0 {
		st := work[len(work)-1]
		work = work[:len(work)-1]
		err, dead := scanRunState(src, l, st, &budget, seen, &work)
		if err != nil {
			return err
		}
		if dead != nil {
			if firstDeath == nil {
				firstDeath = dead
			}
			continue
		}
		finished = true
	}
	if !finished {
		return firstDeath
	}
	return nil
}

// scanRunState scans one reading to the end of the source. The readings it splits off are added
// to work. It returns a limit violation (err), or why this reading is not a program (dead).
func scanRunState(src string, l scriptLimits, st *scriptScanState, budget *int, seen map[string]bool, work *[]*scriptScanState) (err error, dead error) {
	line := func(i int) int { return 1 + strings.Count(src[:min(i, len(src))], "\n") }
	die := func(i int, why string) error {
		return scanError("source line ", line(i), ": ", why)
	}
	top := func() *scanLevel { return &st.levels[len(st.levels)-1] }
	bump := func() error {
		t := top()
		t.chain++
		if t.chain > l.Chain {
			return scanError("source has an expression chain longer than ", l.Chain)
		}
		return nil
	}
	push := func(lv scanLevel) error {
		if len(st.levels) > l.Depth {
			return scanError("source is nested more deeply than ", l.Depth)
		}
		st.levels = append(st.levels, lv)
		return nil
	}
	pop := func(closer byte) (scanLevel, bool) {
		if len(st.levels) <= 1 {
			return scanLevel{}, false
		}
		lv := st.levels[len(st.levels)-1]
		ok := lv.kind == '(' && closer == ')' || lv.kind == '[' && closer == ']' || (lv.kind == '{' || lv.kind == '$') && closer == '}'
		if !ok {
			return scanLevel{}, false
		}
		st.levels = st.levels[:len(st.levels)-1]
		return lv, true
	}
	// scanTemplate skips template text from i up to the closing backtick (returns its index)
	// or to a "${" (returns the index of "{" and true).
	scanTemplate := func(i int) (int, bool, bool) {
		for ; i < len(src); i++ {
			switch src[i] {
			case '\\':
				i++
			case '`':
				return i, false, true
			case '$':
				if i+1 < len(src) && src[i+1] == '{' {
					return i + 1, true, true
				}
			}
		}
		return i, false, false
	}
	afterTemplate := func(expr bool) error {
		if expr {
			st.prev = scanTokExpr
			return push(scanLevel{kind: '$'})
		}
		st.prev = scanTokOperand
		return nil
	}
	isWord := func(c byte) bool {
		return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
	}
	// braceAfter is the class the "}" of a "{" gets, from the token before the "{".
	braceAfter := func() byte {
		switch st.prev {
		case scanTokStart, scanTokControl, scanTokBlockKw, scanTokFnDecl:
			return scanTokStart // a block or a declaration body: a statement follows
		case scanTokFnExpr, scanTokArrow, scanTokExpr, scanTokParen:
			return scanTokOperand // an expression: the body of a function expression, an object
		case scanTokOperand:
			if st.classHdr {
				if st.classStmt {
					return scanTokStart
				}
				return scanTokOperand
			}
		}
		return scanTokAmbigous
	}
	for i := st.pos; i < len(src); i++ {
		*budget--
		if *budget < 0 {
			return scanError("source is too ambiguous to check"), nil
		}
		c := src[i]
		prevDot := st.prevDot
		control := st.control
		wasAsync := st.wasAsync
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return nil, die(i, "unterminated comment")
			}
			i += end + 3
			continue
		}
		st.prevDot, st.control, st.wasAsync = false, false, false
		switch {
		case c == '"' || c == '\'':
			start := i
			closed := false
			for i++; i < len(src) && src[i] != '\n'; i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == c {
					closed = true
					break
				}
			}
			if !closed {
				return nil, die(start, "unterminated string")
			}
			st.prev = scanTokOperand
		case c == '`':
			if err := bump(); err != nil {
				return err, nil
			}
			start := i
			end, expr, ok := scanTemplate(i + 1)
			if !ok {
				return nil, die(start, "unterminated template")
			}
			i = end
			if err := afterTemplate(expr); err != nil {
				return err, nil
			}
		case c == '/':
			mode := scanSlashAfter(st.prev)
			if mode == scanSlashRegex {
				end, ok := scanScriptRegex(src, i)
				if !ok {
					return nil, die(i, "unterminated regular expression")
				}
				*budget -= end - i
				i = end
				st.prev = scanTokOperand
				break
			}
			if mode == scanSlashAmbiguous {
				if end, ok := scanScriptRegex(src, i); ok {
					alt := st.clone()
					alt.pos, alt.prev = end+1, scanTokOperand
					if k := alt.key(); !seen[k] {
						seen[k] = true
						*work = append(*work, alt)
					}
				}
			}
			if err := bump(); err != nil {
				return err, nil
			}
			st.prev = scanTokExpr
		case c == '(' || c == '[':
			if err := bump(); err != nil {
				return err, nil
			}
			lv := scanLevel{kind: c}
			if c == '(' {
				switch {
				case st.pendingFn != 0:
					lv.fn, st.pendingFn = st.pendingFn, 0
				case control:
					lv.fn = scanTokControl
				}
			}
			if err := push(lv); err != nil {
				return err, nil
			}
			st.prev = scanTokExpr
		case c == '{':
			if err := push(scanLevel{kind: '{', after: braceAfter()}); err != nil {
				return err, nil
			}
			st.prev, st.classHdr, st.pendingFn = scanTokStart, false, 0
		case c == ')' || c == ']':
			lv, ok := pop(c)
			if !ok {
				return nil, die(i, "unmatched bracket")
			}
			st.prev = scanTokOperand
			if c == ')' {
				st.prev = scanTokParen
				if lv.fn != 0 {
					st.prev = lv.fn
				}
			}
		case c == '}':
			wasTemplate := len(st.levels) > 1 && top().kind == '$'
			lv, ok := pop(c)
			if !ok {
				return nil, die(i, "unmatched bracket")
			}
			st.prev = lv.after
			if wasTemplate {
				start := i
				end, expr, ok := scanTemplate(i + 1)
				if !ok {
					return nil, die(start, "unterminated template")
				}
				i = end
				if err := afterTemplate(expr); err != nil {
					return err, nil
				}
			} else if st.prev == 0 {
				st.prev = scanTokAmbigous
			}
		case c == ',':
			top().chain = 0
			st.prev = scanTokExpr
		case c == ';':
			top().chain = 0
			st.prev, st.pendingFn = scanTokStart, 0
		case c == '.':
			if err := bump(); err != nil {
				return err, nil
			}
			if i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9' {
				// A number such as .5.
				st.prev = scanTokOperand
			} else {
				st.prev = scanTokExpr
				st.prevDot = true
			}
		case (c == '+' || c == '-') && i+1 < len(src) && src[i+1] == c:
			if err := bump(); err != nil {
				return err, nil
			}
			i++
			// A postfix operator follows an operand and leaves one; a prefix one wants one.
			if st.prev != scanTokOperand && st.prev != scanTokParen {
				st.prev = scanTokExpr
			} else {
				st.prev = scanTokOperand
			}
		case strings.IndexByte("+-*%&|^<>=?:!~", c) >= 0:
			if err := bump(); err != nil {
				return err, nil
			}
			st.prev = scanTokExpr
			if c == '>' && i > 0 && src[i-1] == '=' {
				st.prev = scanTokArrow
			} else if c == ':' {
				st.prev = scanTokColon
			}
		case isWord(c):
			j := i
			for j < len(src) && isWord(src[j]) {
				j++
			}
			word := src[i:j]
			i = j - 1
			if prevDot {
				// A property name: never a keyword.
				st.prev = scanTokOperand
				break
			}
			statement := st.prev == scanTokStart
			switch {
			case scanExprWords[word]:
				if err := bump(); err != nil {
					return err, nil
				}
				st.prev = scanTokExpr
			case scanControlWords[word]:
				if err := bump(); err != nil {
					return err, nil
				}
				st.prev, st.control = scanTokExpr, true
			case scanBlockWords[word]:
				st.prev = scanTokBlockKw
			case scanSoftWords[word]:
				if err := bump(); err != nil {
					return err, nil
				}
				st.prev = scanTokAmbigous
			case word == "function":
				if wasAsync {
					statement = st.asyncStmt
				}
				st.pendingFn = scanTokFnExpr
				if statement {
					st.pendingFn = scanTokFnDecl
				}
				st.prev = scanTokOperand
			case word == "class":
				st.classHdr, st.classStmt = true, statement
				st.prev = scanTokOperand
			case word == "async":
				st.asyncStmt, st.wasAsync = statement, true
				st.prev = scanTokOperand
			default:
				st.prev = scanTokOperand
			}
		}
	}
	if len(st.levels) != 1 {
		return nil, die(len(src), "unclosed bracket")
	}
	return nil, nil
}

// scanScriptRegex returns the index of the last byte of the regular expression literal (flags
// included) that starts at src[i] == '/'. It fails at a line end or the end of the source.
func scanScriptRegex(src string, i int) (int, bool) {
	inClass := false
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '\n', '\r':
			return 0, false
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if inClass {
				continue
			}
			j++
			for j < len(src) && (src[j] == '_' || src[j] == '$' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			return j - 1, true
		}
	}
	return 0, false
}

// scriptRawGuard is the part of the check that does not depend on how the source tokenizes: it
// counts every opening bracket and operator byte, strings and regular expressions included,
// so a source that fools the scan above still cannot nest deeply or chain without bound.
func scriptRawGuard(src string, l scriptLimits) error {
	openers, operators := 0, 0
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '(', '[', '{':
			openers++
			if openers > l.Openers {
				return scanError("source has more than ", l.Openers, " opening brackets")
			}
		case '+', '-', '*', '/', '%', '&', '|', '^', '<', '>', '=', '?', ':', '!', '~':
			operators++
			if operators > l.Operators {
				return scanError("source has more than ", l.Operators, " operators")
			}
		}
	}
	return nil
}
