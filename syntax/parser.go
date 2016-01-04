package syntax

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode"
)

type RegexOptions int32

const (
	IgnoreCase              RegexOptions = 0x0001 // "i"
	Multiline                            = 0x0002 // "m"
	ExplicitCapture                      = 0x0004 // "n"
	Compiled                             = 0x0008 // "c"
	Singleline                           = 0x0010 // "s"
	IgnorePatternWhitespace              = 0x0020 // "x"
	RightToLeft                          = 0x0040 // "r"
	Debug                                = 0x0080 // "d"
	ECMAScript                           = 0x0100 // "e"
)

func optionFromCode(ch rune) RegexOptions {
	// case-insensitive
	switch ch {
	case 'i', 'I':
		return IgnoreCase
	case 'r', 'R':
		return RightToLeft
	case 'm', 'M':
		return Multiline
	case 'n', 'N':
		return ExplicitCapture
	case 's', 'S':
		return Singleline
	case 'x', 'X':
		return IgnorePatternWhitespace
	case 'd', 'D':
		return Debug
	case 'e', 'E':
		return ECMAScript
	default:
		return 0
	}
}

// An Error describes a failure to parse a regular expression
// and gives the offending expression.
type Error struct {
	Code ErrorCode
	Expr string
	Args []interface{}
}

func (e *Error) Error() string {
	if len(e.Args) == 0 {
		return "error parsing regexp: " + e.Code.String() + " in `" + e.Expr + "`"
	}
	return "error parsing regexp: " + fmt.Sprintf(e.Code.String(), e.Args...) + " in `" + e.Expr + "`"
}

// An ErrorCode describes a failure to parse a regular expression.
type ErrorCode string

const (
	// internal issue
	ErrInternalError ErrorCode = "regexp/syntax: internal error"
	// Parser errors
	ErrUnterminatedComment        = "unterminated comment"
	ErrInvalidCharRange           = "invalid character class range"
	ErrInvalidRepeatSize          = "invalid repeat count"
	ErrInvalidUTF8                = "invalid UTF-8"
	ErrCaptureGroupOutOfRange     = "capture group number out of range"
	ErrUnexpectedParen            = "unexpected )"
	ErrMissingParen               = "missing closing )"
	ErrInvalidRepeatOp            = "invalid nested repetition operator"
	ErrMissingRepeatArgument      = "missing argument to repetition operator"
	ErrConditionalExpression      = "illegal conditional (?(...)) expression"
	ErrTooManyAlternates          = "too many | in (?()|)"
	ErrUnrecognizedGrouping       = "unrecognized grouping construct"
	ErrInvalidGroupName           = "invalid group name: group names must begin with a word character"
	ErrCapNumNotZero              = "capture number cannot be zero"
	ErrUndefinedBackRef           = "reference to undefined group number %v"
	ErrUndefinedNameRef           = "reference to undefined group name %v"
	ErrAlternationCantCapture     = "alternation conditions do not capture and cannot be named"
	ErrAlternationCantHaveComment = "alternation conditions cannot be comments"
	ErrMalformedReference         = "(?(%v) ) malformed"
	ErrUndefinedReference         = "(?(%v) ) reference to undefined group"
	ErrIllegalEndEscape           = "illegal \\ at end of pattern"
	ErrMalformedSlashP            = "malformed \\p{X} character escape"
	ErrIncompleteSlashP           = "incomplete \\p{X} character escape"
	ErrUnrecognizedEscape         = "unrecognized escape sequence \\%v"
	ErrMissingControl             = "missing control character"
	ErrUnrecognizedControl        = "unrecognized control character"
	ErrTooFewHex                  = "insufficient hexadecimal digits"
)

func (e ErrorCode) String() string {
	return string(e)
}

type parser struct {
	stack         *regexNode
	group         *regexNode
	alternation   *regexNode
	concatenation *regexNode
	unit          *regexNode

	patternRaw string
	pattern    []rune

	currentPos  int
	specialCase *unicode.SpecialCase

	autocap  int
	capcount int
	captop   int
	capsize  int

	caps     map[int]int
	capnames map[string]int

	capnumlist  []int
	capnamelist []string

	options         RegexOptions
	optionsStack    []RegexOptions
	ignoreNextParen bool
}

const (
	maxValueDiv10 int32 = math.MaxInt32 / 10
	maxValueMod10       = math.MaxInt32 % 10
)

// Parse converts a regex string into a parse tree
func Parse(re string, op RegexOptions) (*RegexTree, error) {
	p := parser{
		options: op,
		caps:    make(map[int]int),
	}
	p.setPattern(re)

	if err := p.countCaptures(); err != nil {
		return nil, err
	}

	p.reset(op)
	root, err := p.scanRegex()

	if err != nil {
		return nil, err
	}
	return &RegexTree{
		root:       root,
		caps:       p.caps,
		capnumlist: p.capnumlist,
		captop:     p.captop,
		Capnames:   p.capnames,
		Caplist:    p.capnamelist,
		options:    op,
	}, nil
}

func (p *parser) setPattern(pattern string) {
	p.patternRaw = pattern
	p.pattern = make([]rune, 0, len(pattern))

	//populate our rune array to handle utf8 encoding
	for _, r := range pattern {
		p.pattern = append(p.pattern, r)
	}
}
func (p *parser) getErr(code ErrorCode, args ...interface{}) error {
	return &Error{Code: code, Expr: p.patternRaw, Args: args}
}

func (p *parser) noteCaptureSlot(i, pos int) {
	if _, ok := p.caps[i]; !ok {
		// the rhs of the hashtable isn't used in the parser
		p.caps[i] = pos
		p.capcount++

		if p.captop <= i {
			if i == math.MaxInt32 {
				p.captop = i
			} else {
				p.captop = i + 1
			}
		}
	}
}

func (p *parser) noteCaptureName(name string, pos int) {
	if p.capnames == nil {
		p.capnames = make(map[string]int)
	}

	if _, ok := p.capnames[name]; !ok {
		p.capnames[name] = pos
		p.capnamelist = append(p.capnamelist, name)
	}
}

func (p *parser) assignNameSlots() {
	if p.capnames != nil {
		for _, name := range p.capnamelist {
			for p.isCaptureSlot(p.autocap) {
				p.autocap++
			}
			pos := p.capnames[name]
			p.capnames[name] = p.autocap
			p.noteCaptureSlot(p.autocap, pos)

			p.autocap++
		}
	}

	// if the caps array has at least one gap, construct the list of used slots
	if p.capcount < p.captop {
		p.capnumlist = make([]int, p.capcount)
		i := 0

		for k := range p.caps {
			p.capnumlist[i] = k
			i++
		}

		sort.Ints(p.capnumlist)
	}

	// merge capsnumlist into capnamelist
	if p.capnames != nil || p.capnumlist != nil {
		var oldcapnamelist []string
		var next int
		var k int

		if p.capnames == nil {
			oldcapnamelist = nil
			p.capnames = make(map[string]int)
			p.capnamelist = []string{}
			next = -1
		} else {
			oldcapnamelist = p.capnamelist
			p.capnamelist = []string{}
			next = p.capnames[oldcapnamelist[0]]
		}

		for i := 0; i < p.capcount; i++ {
			j := i
			if p.capnumlist != nil {
				j = p.capnumlist[i]
			}

			if next == j {
				p.capnamelist = append(p.capnamelist, oldcapnamelist[k])
				k++

				if k == len(oldcapnamelist) {
					next = -1
				} else {
					next = p.capnames[oldcapnamelist[k]]
				}

			} else {
				//feature: culture?
				str := strconv.Itoa(j)
				p.capnamelist = append(p.capnamelist, str)
				p.capnames[str] = j
			}
		}
	}
}

func (p *parser) consumeAutocap() int {
	r := p.autocap
	p.autocap++
	return r
}

// CountCaptures is a prescanner for deducing the slots used for
// captures by doing a partial tokenization of the pattern.
func (p *parser) countCaptures() error {
	var ch rune

	p.noteCaptureSlot(0, 0)

	p.autocap = 1

	for p.charsRight() > 0 {
		pos := p.textpos()
		ch = p.moveRightGetChar()
		switch ch {
		case '\\':
			if p.charsRight() > 0 {
				p.moveRight(1)
			}

		case '#':
			if p.useOptionX() {
				p.moveLeft()
				p.scanBlank()
			}

		case '[':
			p.scanCharClass(false, true)

		case ')':
			if !p.emptyOptionsStack() {
				p.popOptions()
			}

		case '(':
			if p.charsRight() >= 2 && p.rightChar(1) == '#' && p.rightChar(0) == '?' {
				p.moveLeft()
				p.scanBlank()
			} else {
				p.pushOptions()
				if p.charsRight() > 0 && p.rightChar(0) == '?' {
					// we have (?...
					p.moveRight(1)

					if p.charsRight() > 1 && (p.rightChar(0) == '<' || p.rightChar(0) == '\'') {
						// named group: (?<... or (?'...

						p.moveRight(1)
						ch = p.rightChar(0)

						if ch != '0' && IsWordChar(ch) {
							if ch >= '1' && ch <= '9' {
								dec, err := p.scanDecimal()
								if err != nil {
									return err
								}
								p.noteCaptureSlot(dec, pos)
							} else {
								p.noteCaptureName(p.scanCapname(), pos)
							}
						}
					} else {
						// (?...

						// get the options if it's an option construct (?cimsx-cimsx...)
						p.scanOptions()

						if p.charsRight() > 0 {
							if p.rightChar(0) == ')' {
								// (?cimsx-cimsx)
								p.moveRight(1)
								p.popKeepOptions()
							} else if p.rightChar(0) == '(' {
								// alternation construct: (?(foo)yes|no)
								// ignore the next paren so we don't capture the condition
								p.ignoreNextParen = true

								// break from here so we don't reset ignoreNextParen
								continue
							}
						}
					}
				} else {
					if !p.useOptionN() && !p.ignoreNextParen {
						p.noteCaptureSlot(p.consumeAutocap(), pos)
					}
				}
			}

			p.ignoreNextParen = false

		}
	}

	p.assignNameSlots()
	return nil
}

func (p *parser) reset(topopts RegexOptions) {
	p.currentPos = 0
	p.autocap = 1
	p.ignoreNextParen = false

	if len(p.optionsStack) > 0 {
		p.optionsStack = p.optionsStack[:0]
	}

	p.options = topopts
	p.stack = nil
}

func (p *parser) scanRegex() (*regexNode, error) {
	ch := '@' // nonspecial ch, means at beginning
	isQuant := false

	p.startGroup(newRegexNodeMN(ntCapture, p.options, 0, -1))

	for p.charsRight() > 0 {
		wasPrevQuantifier := isQuant
		isQuant = false

		if err := p.scanBlank(); err != nil {
			return nil, err
		}

		startpos := p.textpos()

		// move past all of the normal characters.  We'll stop when we hit some kind of control character,
		// or if IgnorePatternWhiteSpace is on, we'll stop when we see some whitespace.
		if p.useOptionX() {
			for p.charsRight() > 0 {
				ch = p.rightChar(0)
				//UGLY: clean up, this is ugly
				if !(!isStopperX(ch) || (ch == '{' && !p.isTrueQuantifier())) {
					break
				}
				p.moveRight(1)
			}
		} else {
			for p.charsRight() > 0 {
				ch = p.rightChar(0)
				if !(!isSpecial(ch) || ch == '{' && !p.isTrueQuantifier()) {
					break
				}
				p.moveRight(1)
			}
		}

		endpos := p.textpos()

		p.scanBlank()

		if p.charsRight() == 0 {
			ch = '!' // nonspecial, means at end
		} else if ch = p.rightChar(0); isSpecial(ch) {
			isQuant = isQuantifier(ch)
			p.moveRight(1)
		} else {
			ch = ' ' // nonspecial, means at ordinary char
		}

		if startpos < endpos {
			cchUnquantified := endpos - startpos
			if isQuant {
				cchUnquantified--
			}
			wasPrevQuantifier = false

			if cchUnquantified > 0 {
				p.addToConcatenate(startpos, cchUnquantified, false)
			}

			if isQuant {
				p.addUnitOne(p.charAt(endpos - 1))
			}
		}

		switch ch {
		case '!':
			goto BreakOuterScan

		case ' ':
			goto ContinueOuterScan

		case '[':
			cc, err := p.scanCharClass(p.useOptionI(), false)
			if err != nil {
				return nil, err
			}
			p.addUnitSet(cc.toStringClass())

		case '(':
			p.pushOptions()

			if grouper, err := p.scanGroupOpen(); err != nil {
				return nil, err
			} else if grouper == nil {
				p.popKeepOptions()
			} else {
				p.pushGroup()
				p.startGroup(grouper)
			}

			continue

		case '|':
			p.addAlternate()
			goto ContinueOuterScan

		case ')':
			if p.emptyStack() {
				return nil, p.getErr(ErrUnexpectedParen)
			}

			if err := p.addGroup(); err != nil {
				return nil, err
			}
			if err := p.popGroup(); err != nil {
				return nil, err
			}
			p.popOptions()

			if p.unit == nil {
				goto ContinueOuterScan
			}

		case '\\':
			n, err := p.scanBackslash()
			if err != nil {
				return nil, err
			}
			p.addUnitNode(n)

		case '^':
			if p.useOptionM() {
				p.addUnitType(ntBol)
			} else {
				p.addUnitType(ntBeginning)
			}

		case '$':
			if p.useOptionM() {
				p.addUnitType(ntEol)
			} else {
				p.addUnitType(ntEndZ)
			}

		case '.':
			if p.useOptionS() {
				p.addUnitSet(AnyClass)
			} else {
				p.addUnitNotone('\n')
			}

		case '{', '*', '+', '?':
			if p.unit == nil {
				if wasPrevQuantifier {
					return nil, p.getErr(ErrInvalidRepeatOp)
				} else {
					return nil, p.getErr(ErrMissingRepeatArgument)
				}
			}
			p.moveLeft()

		default:
			return nil, p.getErr(ErrInternalError)
		}

		if err := p.scanBlank(); err != nil {
			return nil, err
		}

		if p.charsRight() == 0 || !p.isTrueQuantifier() {
			//maintain odd C# assignment order -- not sure if required, could clean up?
			if p.charsRight() != 0 {
				isQuant = p.isTrueQuantifier()
			}
			p.addConcatenate()
			goto ContinueOuterScan
		}

		ch = p.moveRightGetChar()

		// Handle quantifiers
		for p.unit != nil {
			var min, max int
			var lazy bool

			switch ch {
			case '*':
				min = 0
				max = math.MaxInt32

			case '?':
				min = 0
				max = 1

			case '+':
				min = 1
				max = math.MaxInt32

			case '{':
				{
					var err error
					startpos = p.textpos()
					if min, err = p.scanDecimal(); err != nil {
						return nil, err
					}
					max = min
					if startpos < p.textpos() {
						if p.charsRight() > 0 && p.rightChar(0) == ',' {
							p.moveRight(1)
							if p.charsRight() == 0 || p.rightChar(0) == '}' {
								max = math.MaxInt32
							} else {
								if max, err = p.scanDecimal(); err != nil {
									return nil, err
								}
							}
						}
					}

					if startpos == p.textpos() || p.charsRight() == 0 || p.moveRightGetChar() != '}' {
						p.addConcatenate()
						p.textto(startpos - 1)
						goto ContinueOuterScan
					}
				}

			default:
				return nil, p.getErr(ErrInternalError)
			}

			if err := p.scanBlank(); err != nil {
				return nil, err
			}

			if p.charsRight() == 0 || p.rightChar(0) != '?' {
				lazy = false
			} else {
				p.moveRight(1)
				lazy = true
			}

			if min > max {
				return nil, p.getErr(ErrInvalidRepeatSize)
			}

			p.addConcatenate3(lazy, min, max)
		}

	ContinueOuterScan:
	}

BreakOuterScan:
	;

	if !p.emptyStack() {
		return nil, p.getErr(ErrMissingParen)
	}

	if err := p.addGroup(); err != nil {
		return nil, err
	}

	return p.unit, nil

}

// scanGroupOpen scans chars following a '(' (not counting the '('), and returns
// a RegexNode for the type of group scanned, or nil if the group
// simply changed options (?cimsx-cimsx) or was a comment (#...).
func (p *parser) scanGroupOpen() (*regexNode, error) {
	var ch rune
	var nt nodeType
	var err error
	close := '>'

	// just return a RegexNode if we have:
	// 1. "(" followed by nothing
	// 2. "(x" where x != ?
	// 3. "(?)"
	if p.charsRight() == 0 || p.rightChar(0) != '?' || (p.rightChar(0) == '?' && (p.charsRight() > 1 && p.rightChar(1) == ')')) {
		if p.useOptionN() || p.ignoreNextParen {
			p.ignoreNextParen = false
			return newRegexNode(ntGroup, p.options), nil
		}
		return newRegexNodeMN(ntCapture, p.options, p.consumeAutocap(), -1), nil
	}

	p.moveRight(1)

	for {
		if p.charsRight() == 0 {
			break
		}

		switch ch = p.moveRightGetChar(); ch {
		case ':':
			nt = ntGroup

		case '=':
			p.options &= ^RightToLeft
			nt = ntRequire

		case '!':
			p.options &= ^RightToLeft
			nt = ntPrevent

		case '>':
			nt = ntGreedy

		case '\'':
			close = '\''
			fallthrough

		case '<':
			if p.charsRight() == 0 {
				goto BreakRecognize
			}

			switch ch = p.moveRightGetChar(); ch {
			case '=':
				if close == '\'' {
					goto BreakRecognize
				}

				p.options |= RightToLeft
				nt = ntRequire

			case '!':
				if close == '\'' {
					goto BreakRecognize
				}

				p.options |= RightToLeft
				nt = ntPrevent

			default:
				p.moveLeft()
				capnum := -1
				uncapnum := -1
				proceed := false

				// grab part before -

				if ch >= '0' && ch <= '9' {
					if capnum, err = p.scanDecimal(); err != nil {
						return nil, err
					}

					if !p.isCaptureSlot(capnum) {
						capnum = -1
					}

					// check if we have bogus characters after the number
					if p.charsRight() > 0 && !(p.rightChar(0) == close || p.rightChar(0) == '-') {
						return nil, p.getErr(ErrInvalidGroupName)
					}
					if capnum == 0 {
						return nil, p.getErr(ErrCapNumNotZero)
					}
				} else if IsWordChar(ch) {
					capname := p.scanCapname()

					if p.isCaptureName(capname) {
						capnum = p.captureSlotFromName(capname)
					}

					// check if we have bogus character after the name
					if p.charsRight() > 0 && !(p.rightChar(0) == close || p.rightChar(0) == '-') {
						return nil, p.getErr(ErrInvalidGroupName)
					}
				} else if ch == '-' {
					proceed = true
				} else {
					// bad group name - starts with something other than a word character and isn't a number
					return nil, p.getErr(ErrInvalidGroupName)
				}

				// grab part after - if any

				if (capnum != -1 || proceed == true) && p.charsRight() > 0 && p.rightChar(0) == '-' {
					p.moveRight(1)
					ch = p.rightChar(0)

					if ch >= '0' && ch <= '9' {
						if uncapnum, err = p.scanDecimal(); err != nil {
							return nil, err
						}

						if !p.isCaptureSlot(uncapnum) {
							return nil, p.getErr(ErrUndefinedBackRef, uncapnum)
						}

						// check if we have bogus characters after the number
						if p.charsRight() > 0 && p.rightChar(0) != close {
							return nil, p.getErr(ErrInvalidGroupName)
						}
					} else if IsWordChar(ch) {
						uncapname := p.scanCapname()

						if !p.isCaptureName(uncapname) {
							return nil, p.getErr(ErrUndefinedNameRef, uncapname)
						}
						uncapnum = p.captureSlotFromName(uncapname)

						// check if we have bogus character after the name
						if p.charsRight() > 0 && p.rightChar(0) != close {
							return nil, p.getErr(ErrInvalidGroupName)
						}
					} else {
						// bad group name - starts with something other than a word character and isn't a number
						return nil, p.getErr(ErrInvalidGroupName)
					}
				}

				// actually make the node

				if (capnum != -1 || uncapnum != -1) && p.charsRight() > 0 && p.moveRightGetChar() == close {
					return newRegexNodeMN(ntCapture, p.options, capnum, uncapnum), nil
				}
				goto BreakRecognize
			}

		case '(':
			// alternation construct (?(...) | )

			parenPos := p.textpos()
			if p.charsRight() > 0 {
				ch = p.rightChar(0)

				// check if the alternation condition is a backref
				if ch >= '0' && ch <= '9' {
					var capnum int
					if capnum, err = p.scanDecimal(); err != nil {
						return nil, err
					}
					if p.charsRight() > 0 && p.moveRightGetChar() == ')' {
						if p.isCaptureSlot(capnum) {
							return newRegexNodeM(ntTestref, p.options, capnum), nil
						}
						return nil, p.getErr(ErrUndefinedReference, capnum)
					}

					return nil, p.getErr(ErrMalformedReference, capnum)

				} else if IsWordChar(ch) {
					capname := p.scanCapname()

					if p.isCaptureName(capname) && p.charsRight() > 0 && p.moveRightGetChar() == ')' {
						return newRegexNodeM(ntTestref, p.options, p.captureSlotFromName(capname)), nil
					}
				}
			}
			// not a backref
			nt = ntTestgroup
			p.textto(parenPos - 1)   // jump to the start of the parentheses
			p.ignoreNextParen = true // but make sure we don't try to capture the insides

			charsRight := p.charsRight()
			if charsRight >= 3 && p.rightChar(1) == '?' {
				rightchar2 := p.rightChar(2)
				// disallow comments in the condition
				if rightchar2 == '#' {
					return nil, p.getErr(ErrAlternationCantHaveComment)
				}

				// disallow named capture group (?<..>..) in the condition
				if rightchar2 == '\'' {
					return nil, p.getErr(ErrAlternationCantCapture)
				}

				if charsRight >= 4 && (rightchar2 == '<' && p.rightChar(3) != '!' && p.rightChar(3) != '=') {
					return nil, p.getErr(ErrAlternationCantCapture)
				}
			}

		default:
			p.moveLeft()

			nt = ntGroup
			p.scanOptions()
			if p.charsRight() == 0 {
				goto BreakRecognize
			}

			if ch = p.moveRightGetChar(); ch == ')' {
				return nil, nil
			}

			if ch != ':' {
				goto BreakRecognize
			}

		}

		return newRegexNode(nt, p.options), nil
	}

BreakRecognize:

	// break Recognize comes here

	return nil, p.getErr(ErrUnrecognizedGrouping)
}

// scans backslash specials and basics
func (p *parser) scanBackslash() (*regexNode, error) {

	if p.charsRight() == 0 {
		return nil, p.getErr(ErrIllegalEndEscape)
	}

	switch ch := p.rightChar(0); ch {
	case 'b', 'B', 'A', 'G', 'Z', 'z':
		p.moveRight(1)
		return newRegexNode(p.typeFromCode(ch), p.options), nil

	case 'w':
		p.moveRight(1)
		if p.useOptionE() {
			return newRegexNodeStr(ntSet, p.options, ECMAWordClass), nil
		}
		return newRegexNodeStr(ntSet, p.options, WordClass), nil

	case 'W':
		p.moveRight(1)
		if p.useOptionE() {
			return newRegexNodeStr(ntSet, p.options, NotECMAWordClass), nil
		}
		return newRegexNodeStr(ntSet, p.options, NotWordClass), nil

	case 's':
		p.moveRight(1)
		if p.useOptionE() {
			return newRegexNodeStr(ntSet, p.options, ECMASpaceClass), nil
		}
		return newRegexNodeStr(ntSet, p.options, SpaceClass), nil

	case 'S':
		p.moveRight(1)
		if p.useOptionE() {
			return newRegexNodeStr(ntSet, p.options, NotECMASpaceClass), nil
		}
		return newRegexNodeStr(ntSet, p.options, NotSpaceClass), nil

	case 'd':
		p.moveRight(1)
		if p.useOptionE() {
			return newRegexNodeStr(ntSet, p.options, ECMADigitClass), nil
		}
		return newRegexNodeStr(ntSet, p.options, DigitClass), nil

	case 'D':
		p.moveRight(1)
		if p.useOptionE() {
			return newRegexNodeStr(ntSet, p.options, NotECMADigitClass), nil
		}
		return newRegexNodeStr(ntSet, p.options, NotDigitClass), nil

	case 'p', 'P':
		p.moveRight(1)
		cc := newCharClass()
		prop, err := p.parseProperty()
		if err != nil {
			return nil, err
		}
		cc.addCategoryFromName(prop, (ch != 'p'), p.useOptionI(), p.patternRaw)
		if p.useOptionI() {
			cc.addLowercase()
		}

		return newRegexNodeStr(ntSet, p.options, cc.toStringClass()), nil

	default:
		return p.scanBasicBackslash()
	}
}

func (p *parser) scanBasicBackslash() (*regexNode, error) {
	panic("not implemented")
}

// Scans X for \p{X} or \P{X}
func (p *parser) parseProperty() (string, error) {
	if p.charsRight() < 3 {
		return "", p.getErr(ErrIncompleteSlashP)
	}
	ch := p.moveRightGetChar()
	if ch != '{' {
		return "", p.getErr(ErrMalformedSlashP)
	}

	startpos := p.textpos()
	for p.charsRight() > 0 {
		ch = p.moveRightGetChar()
		if !(IsWordChar(ch) || ch == '-') {
			p.moveLeft()
			break
		}
	}
	capname := string(p.pattern[startpos:p.textpos()])

	if p.charsRight() == 0 || p.moveRightGetChar() != '}' {
		return "", p.getErr(ErrIncompleteSlashP)
	}

	return capname, nil
}

// Returns ReNode type for zero-length assertions with a \ code.
func (p *parser) typeFromCode(ch rune) nodeType {
	switch ch {
	case 'b':
		if p.useOptionE() {
			return ntECMABoundary
		}
		return ntBoundary
	case 'B':
		if p.useOptionE() {
			return ntNonECMABoundary
		}
		return ntNonboundary
	case 'A':
		return ntBeginning
	case 'G':
		return ntStart
	case 'Z':
		return ntEndZ
	case 'z':
		return ntEnd
	default:
		return ntNothing
	}
}

// Scans whitespace or x-mode comments.
func (p *parser) scanBlank() error {
	if p.useOptionX() {
		for {
			for p.charsRight() > 0 && isSpace(p.rightChar(0)) {
				p.moveRight(1)
			}

			if p.charsRight() == 0 {
				break
			}

			if p.rightChar(0) == '#' {
				for p.charsRight() > 0 && p.rightChar(0) != '\n' {
					p.moveRight(1)
				}
			} else if p.charsRight() >= 3 && p.rightChar(2) == '#' &&
				p.rightChar(1) == '?' && p.rightChar(0) == '(' {
				for p.charsRight() > 0 && p.rightChar(0) != ')' {
					p.moveRight(1)
				}
				if p.charsRight() == 0 {
					return p.getErr(ErrUnterminatedComment)
				}
				p.moveRight(1)
			} else {
				break
			}
		}
	} else {
		for {
			if p.charsRight() < 3 || p.rightChar(2) != '#' ||
				p.rightChar(1) != '?' || p.rightChar(0) != '(' {
				return nil
			}

			for p.charsRight() > 0 && p.rightChar(0) != ')' {
				p.moveRight(1)
			}
			if p.charsRight() == 0 {
				return p.getErr(ErrUnterminatedComment)
			}
			p.moveRight(1)
		}
	}
	return nil
}

func (p *parser) scanCapname() string {
	startpos := p.textpos()

	for p.charsRight() > 0 {
		if !IsWordChar(p.moveRightGetChar()) {
			p.moveLeft()
			break
		}
	}

	return string(p.pattern[startpos:p.textpos()])
}

//Scans contents of [] (not including []'s), and converts to a charClass.
func (p *parser) scanCharClass(caseInsensitive, scanOnly bool) (*charClass, error) {
	//TOOD: scanCharClass
	return nil, nil
}

// Scans any number of decimal digits (pegs value at 2^31-1 if too large)
func (p *parser) scanDecimal() (int, error) {
	i := int32(0)
	var d int32

	for p.charsRight() > 0 {
		d = p.rightChar(0) - '0'
		if d < 0 || d > 9 {
			break
		}
		p.moveRight(1)

		if i > (maxValueDiv10) || (i == (maxValueDiv10) && d > (maxValueMod10)) {
			return 0, p.getErr(ErrCaptureGroupOutOfRange)
		}

		i *= 10
		i += d
	}

	return int(i), nil
}

// Returns true for options allowed only at the top level
func isOnlyTopOption(option RegexOptions) bool {
	return option == RightToLeft || option == ECMAScript
}

// Scans cimsx-cimsx option string, stops at the first unrecognized char.
func (p *parser) scanOptions() {

	for off := false; p.charsRight() > 0; p.moveRight(1) {
		ch := p.rightChar(0)

		if ch == '-' {
			off = true
		} else if ch == '+' {
			off = false
		} else {
			option := optionFromCode(ch)
			if option == 0 || isOnlyTopOption(option) {
				return
			}

			if off {
				p.options &= ^option
			} else {
				p.options |= option
			}
		}
	}
}

// Scans \ code for escape codes that map to single unicode chars.
func (p *parser) scanCharEscape() (rune, error) {

	ch := p.moveRightGetChar()

	if ch >= '0' && ch <= '7' {
		p.moveLeft()
		return p.scanOctal(), nil
	}

	switch ch {
	case 'x':
		return p.scanHex(2)
	case 'u':
		return p.scanHex(4)
	case 'a':
		return '\u0007', nil
	case 'b':
		return '\b', nil
	case 'e':
		return '\u001B', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'v':
		return '\u000B', nil
	case 'c':
		return p.scanControl()
	default:
		if !p.useOptionE() && IsWordChar(ch) {
			return 0, p.getErr(ErrUnrecognizedEscape, ch)
		}
		return ch, nil
	}
}

// Grabs and converts an ascii control character
func (p *parser) scanControl() (rune, error) {
	if p.charsRight() <= 0 {
		return 0, p.getErr(ErrMissingControl)
	}

	ch := p.moveRightGetChar()

	// \ca interpreted as \cA

	if ch >= 'a' && ch <= 'z' {
		ch = (ch - ('a' - 'A'))
	}
	ch = (ch - '@')
	if ch < ' ' {
		return ch, nil
	}

	return 0, p.getErr(ErrUnrecognizedControl)

}

// Scans exactly c hex digits (c=2 for \xFF, c=4 for \uFFFF)
func (p *parser) scanHex(c int) (rune, error) {

	i := 0

	if p.charsRight() >= c {
		d := hexDigit(p.moveRightGetChar())
		for ; c > 0 && d >= 0; c -= 1 {
			i *= 0x10
			i += d
			d = hexDigit(p.moveRightGetChar())
		}
	}

	if c > 0 {
		return 0, p.getErr(ErrTooFewHex)
	}

	return rune(i), nil
}

// Returns n <= 0xF for a hex digit.
func hexDigit(ch rune) int {

	if d := int(ch - '0'); d <= 9 {
		return d
	}

	if d := int(ch - 'a'); d <= 5 {
		return d + 0xa
	}

	if d := int(ch - 'A'); d <= 5 {
		return d + 0xa
	}

	return -1
}

// Scans up to three octal digits (stops before exceeding 0377).
func (p *parser) scanOctal() rune {
	// Consume octal chars only up to 3 digits and value 0377

	c := 3

	if c > p.charsRight() {
		c = p.charsRight()
	}

	i := 0
	d := int(p.rightChar(0) - '0')
	for ; c > 0 && d <= 7; c -= 1 {
		p.moveRight(1)
		i *= 8
		i += d
		if p.useOptionE() && i >= 0x20 {
			break
		}
		d = int(p.rightChar(0) - '0')
	}

	// Octal codes only go up to 255.  Any larger and the behavior that Perl follows
	// is simply to truncate the high bits.
	i &= 0xFF

	return rune(i)
}

// Returns the current parsing position.
func (p *parser) textpos() int {
	return p.currentPos
}

// Zaps to a specific parsing position.
func (p *parser) textto(pos int) {
	p.currentPos = pos
}

// Returns the char at the right of the current parsing position and advances to the right.
func (p *parser) moveRightGetChar() rune {
	ch := p.pattern[p.currentPos]
	p.currentPos++
	return ch
}

// Moves the current position to the right.
func (p *parser) moveRight(i int) {
	// default would be 1
	p.currentPos += i
}

// Moves the current parsing position one to the left.
func (p *parser) moveLeft() {
	p.currentPos--
}

// Returns the char left of the current parsing position.
func (p *parser) charAt(i int) rune {
	return p.pattern[i]
}

// Returns the char i chars right of the current parsing position.
func (p *parser) rightChar(i int) rune {
	// default would be 0
	return p.pattern[p.currentPos+i]
}

// Number of characters to the right of the current parsing position.
func (p *parser) charsRight() int {
	return len(p.pattern) - p.currentPos
}

func (p *parser) rightMost() bool {
	return p.currentPos == len(p.pattern)
}

// Looks up the slot number for a given name
func (p *parser) captureSlotFromName(capname string) int {
	return p.capnames[capname]
}

// True if the capture slot was noted
func (p *parser) isCaptureSlot(i int) bool {
	if p.caps != nil {
		_, ok := p.caps[i]
		return ok
	}

	return (i >= 0 && i < p.capsize)
}

// Looks up the slot number for a given name
func (p *parser) isCaptureName(capname string) bool {
	if p.capnames == nil {
		return false
	}

	_, ok := p.capnames[capname]
	return ok
}

// option shortcuts

// True if N option disabling '(' autocapture is on.
func (p *parser) useOptionN() bool {
	return (p.options & ExplicitCapture) != 0
}

// True if I option enabling case-insensitivity is on.
func (p *parser) useOptionI() bool {
	return (p.options & IgnoreCase) != 0
}

// True if M option altering meaning of $ and ^ is on.
func (p *parser) useOptionM() bool {
	return (p.options & Multiline) != 0
}

// True if S option altering meaning of . is on.
func (p *parser) useOptionS() bool {
	return (p.options & Singleline) != 0
}

// True if X option enabling whitespace/comment mode is on.
func (p *parser) useOptionX() bool {
	return (p.options & IgnorePatternWhitespace) != 0
}

// True if E option enabling ECMAScript behavior on.
func (p *parser) useOptionE() bool {
	return (p.options & ECMAScript) != 0
}

// True if options stack is empty.
func (p *parser) emptyOptionsStack() bool {
	return len(p.optionsStack) == 0
}

// Finish the current quantifiable (when a quantifier is not found or is not possible)
func (p *parser) addConcatenate() {
	// The first (| inside a Testgroup group goes directly to the group
	p.concatenation.addChild(p.unit)
	p.unit = nil
}

// Finish the current quantifiable (when a quantifier is found)
func (p *parser) addConcatenate3(lazy bool, min, max int) {
	p.concatenation.addChild(p.unit.makeQuantifier(lazy, min, max))
	p.unit = nil
}

// Sets the current unit to a single char node
func (p *parser) addUnitOne(ch rune) {
	if p.useOptionI() {
		ch = unicode.ToLower(ch)
	}

	p.unit = newRegexNodeCh(ntOne, p.options, ch)
}

// Sets the current unit to a single inverse-char node
func (p *parser) addUnitNotone(ch rune) {
	if p.useOptionI() {
		ch = unicode.ToLower(ch)
	}

	p.unit = newRegexNodeCh(ntNotone, p.options, ch)
}

// Sets the current unit to a single set node
func (p *parser) addUnitSet(cc string) {
	p.unit = newRegexNodeStr(ntSet, p.options, cc)
}

// Sets the current unit to a subtree
func (p *parser) addUnitNode(node *regexNode) {
	p.unit = node
}

// Sets the current unit to an assertion of the specified type
func (p *parser) addUnitType(t nodeType) {
	p.unit = newRegexNode(t, p.options)
}

// Finish the current group (in response to a ')' or end)
func (p *parser) addGroup() error {
	if p.group.t == ntTestgroup || p.group.t == ntTestref {
		p.group.addChild(p.concatenation.reverseLeft())

		if (p.group.t == ntTestref && len(p.group.children) > 2) || len(p.group.children) > 3 {
			return p.getErr(ErrTooManyAlternates)
		}
	} else {
		p.alternation.addChild(p.concatenation.reverseLeft())
		p.group.addChild(p.alternation)
	}

	p.unit = p.group
	return nil
}

// Pops the option stack, but keeps the current options unchanged.
func (p *parser) popKeepOptions() {
	lastIdx := len(p.optionsStack) - 1
	p.optionsStack = p.optionsStack[:lastIdx]
}

// Recalls options from the stack.
func (p *parser) popOptions() {
	lastIdx := len(p.optionsStack) - 1
	// get the last item on the stack and then remove it by reslicing
	p.options = p.optionsStack[lastIdx]
	p.optionsStack = p.optionsStack[:lastIdx]
}

// Saves options on a stack.
func (p *parser) pushOptions() {
	p.optionsStack = append(p.optionsStack, p.options)
}

// Add a string to the last concatenate.
func (p *parser) addToConcatenate(pos, cch int, isReplacement bool) {
	var node *regexNode

	if cch == 0 {
		return
	}

	if cch > 1 {
		str := p.pattern[pos : pos+cch]

		if p.useOptionI() && !isReplacement {
			// We do the ToLower character by character for consistency.  With surrogate chars, doing
			// a ToLower on the entire string could actually change the surrogate pair.  This is more correct
			// linguistically, but since Regex doesn't support surrogates, it's more important to be
			// consistent.
			for i := 0; i < len(str); i++ {
				str[i] = unicode.ToLower(str[i])
			}
		}

		node = newRegexNodeStr(ntMulti, p.options, string(str))
	} else {
		ch := p.charAt(pos)

		if p.useOptionI() && !isReplacement {
			ch = unicode.ToLower(ch)
		}

		node = newRegexNodeCh(ntOne, p.options, ch)
	}

	p.concatenation.addChild(node)
}

// Push the parser state (in response to an open paren)
func (p *parser) pushGroup() {
	p.group.next = p.stack
	p.alternation.next = p.group
	p.concatenation.next = p.alternation
	p.stack = p.concatenation
}

// Remember the pushed state (in response to a ')')
func (p *parser) popGroup() error {
	p.concatenation = p.stack
	p.alternation = p.concatenation.next
	p.group = p.alternation.next
	p.stack = p.group.next

	// The first () inside a Testgroup group goes directly to the group
	if p.group.t == ntTestgroup && len(p.group.children) == 0 {
		if p.unit == nil {
			return p.getErr(ErrConditionalExpression)
		}

		p.group.addChild(p.unit)
		p.unit = nil
	}
	return nil
}

// True if the group stack is empty.
func (p *parser) emptyStack() bool {
	return p.stack == nil
}

// Start a new round for the parser state (in response to an open paren or string start)
func (p *parser) startGroup(openGroup *regexNode) {
	p.group = openGroup
	p.alternation = newRegexNode(ntAlternate, p.options)
	p.concatenation = newRegexNode(ntConcatenate, p.options)
}

// Finish the current concatenation (in response to a |)
func (p *parser) addAlternate() {
	// The | parts inside a Testgroup group go directly to the group

	if p.group.t == ntTestgroup || p.group.t == ntTestref {
		p.group.addChild(p.concatenation.reverseLeft())
	} else {
		p.alternation.addChild(p.concatenation.reverseLeft())
	}

	p.concatenation = newRegexNode(ntConcatenate, p.options)
}

// For categorizing ascii characters.

const (
	Q byte = 5 // quantifier
	S      = 4 // ordinary stopper
	Z      = 3 // ScanBlank stopper
	X      = 2 // whitespace
	E      = 1 // should be escaped
)

var _category = []byte{
	//01  2  3  4  5  6  7  8  9  A  B  C  D  E  F  0  1  2  3  4  5  6  7  8  9  A  B  C  D  E  F
	0, 0, 0, 0, 0, 0, 0, 0, 0, X, X, 0, X, X, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// !  "  #  $  %  &  '  (  )  *  +  ,  -  .  /  0  1  2  3  4  5  6  7  8  9  :  ;  <  =  >  ?
	X, 0, 0, Z, S, 0, 0, 0, S, S, Q, Q, 0, 0, S, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, Q,
	//@A  B  C  D  E  F  G  H  I  J  K  L  M  N  O  P  Q  R  S  T  U  V  W  X  Y  Z  [  \  ]  ^  _
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, S, S, 0, S, 0,
	//'a  b  c  d  e  f  g  h  i  j  k  l  m  n  o  p  q  r  s  t  u  v  w  x  y  z  {  |  }  ~
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, Q, S, 0, 0, 0,
}

func isSpace(ch rune) bool {
	return (ch <= ' ' && _category[ch] == X)
}

// Returns true for those characters that terminate a string of ordinary chars.
func isSpecial(ch rune) bool {
	return (ch <= '|' && _category[ch] >= S)
}

// Returns true for those characters that terminate a string of ordinary chars.
func isStopperX(ch rune) bool {
	return (ch <= '|' && _category[ch] >= X)
}

// Returns true for those characters that begin a quantifier.
func isQuantifier(ch rune) bool {
	return (ch <= '{' && _category[ch] >= Q)
}

func (p *parser) isTrueQuantifier() bool {
	nChars := p.charsRight()
	if nChars == 0 {
		return false
	}

	startpos := p.textpos()
	ch := p.charAt(startpos)
	if ch != '{' {
		return ch <= '{' && _category[ch] >= Q
	}

	//UGLY: this is ugly -- the original code was ugly too
	pos := startpos
	for {
		nChars--
		if nChars <= 0 {
			break
		}
		pos++
		ch = p.charAt(pos)
		if ch < '0' || ch > '9' {
			break
		}
	}

	if nChars == 0 || pos-startpos == 1 {
		return false
	}
	if ch == '}' {
		return true
	}
	if ch != ',' {
		return false
	}
	for {
		nChars--
		if nChars <= 0 {
			break
		}
		pos++
		ch = p.charAt(pos)
		if ch < '0' || ch > '9' {
			break
		}
	}

	return nChars > 0 && ch == '}'
}
