package parse

import (
	"fmt"
	"strings"
	"unicode"
)

// tokenKind enumerates everything the M2 grammar can see. Keywords get
// their own kinds so the parser switches on kind, not string.
type tokenKind int

const (
	tEOF tokenKind = iota
	tIdent
	tString
	tNumber
	tParam // $N

	// punctuation
	tLParen
	tRParen
	tComma
	tSemi
	tStar
	tDot
	tPlus
	tMinus
	tSlash
	tPercent
	tConcat // ||
	tCast   // ::
	tEq
	tNeq // both != and <>
	tLt
	tGt
	tLte
	tGte

	// keywords
	kwSelect
	kwFrom
	kwWhere
	kwAnd
	kwOr
	kwNot
	kwNull
	kwOrder
	kwBy
	kwAsc
	kwDesc
	kwLimit
	kwOffset
	kwInsert
	kwInto
	kwValues
	kwCreate
	kwTable
	kwReturning
	kwTrue
	kwFalse
	kwAs
	kwPrimary
	kwKey
	kwUnique
	kwCheck
	kwDelete
	kwUpdate
	kwSet
	kwJoin
	kwInner
	kwOn
	kwLeft
	kwOuter
	kwCross
	kwIn
	kwReferences
	kwCascade
	kwDrop
	kwIf
	kwExists
	kwGroup
	kwHaving
	kwDistinct
	kwLike
	kwIlike
	kwIs
	kwBetween
)

type token struct {
	kind tokenKind
	val  string // raw text
	pos  int    // byte offset in input
}

// keywords maps the canonical lower-case spelling to its kind.
var keywords = map[string]tokenKind{
	"select":     kwSelect,
	"from":       kwFrom,
	"where":      kwWhere,
	"and":        kwAnd,
	"or":         kwOr,
	"not":        kwNot,
	"null":       kwNull,
	"order":      kwOrder,
	"by":         kwBy,
	"asc":        kwAsc,
	"desc":       kwDesc,
	"limit":      kwLimit,
	"offset":     kwOffset,
	"insert":     kwInsert,
	"into":       kwInto,
	"values":     kwValues,
	"create":     kwCreate,
	"table":      kwTable,
	"returning":  kwReturning,
	"true":       kwTrue,
	"false":      kwFalse,
	"as":         kwAs,
	"primary":    kwPrimary,
	"key":        kwKey,
	"unique":     kwUnique,
	"check":      kwCheck,
	"delete":     kwDelete,
	"update":     kwUpdate,
	"set":        kwSet,
	"join":       kwJoin,
	"inner":      kwInner,
	"on":         kwOn,
	"left":       kwLeft,
	"outer":      kwOuter,
	"cross":      kwCross,
	"in":         kwIn,
	"references": kwReferences,
	"cascade":    kwCascade,
	"drop":       kwDrop,
	"if":         kwIf,
	"exists":     kwExists,
	"group":      kwGroup,
	"having":     kwHaving,
	"distinct":   kwDistinct,
	"like":       kwLike,
	"ilike":      kwIlike,
	"is":         kwIs,
	"between":    kwBetween,
}

// lex turns SQL into a token stream. We tokenize eagerly; the input is
// always small enough (one statement) that streaming buys nothing.
// singleByteTokens is the punctuation that has no two-character form:
// every entry is a single-byte token kind. Pulled out of lex's switch
// so the dispatch stays small (gocyclo).
var singleByteTokens = map[byte]tokenKind{
	'(': tLParen,
	')': tRParen,
	',': tComma,
	';': tSemi,
	'*': tStar,
	'.': tDot,
	'+': tPlus,
	'/': tSlash,
	'%': tPercent,
	'=': tEq,
}

func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			i = skipLineComment(src, i)
		case c == '-':
			out = append(out, token{tMinus, "-", i})
			i++
		case c == '<':
			tok, n := lexLt(src, i)
			out = append(out, tok)
			i += n
		case c == '>':
			tok, n := lexGt(src, i)
			out = append(out, tok)
			i += n
		case c == '!':
			if i+1 >= len(src) || src[i+1] != '=' {
				return nil, fmt.Errorf("lex: stray '!' at %d", i)
			}
			out = append(out, token{tNeq, "!=", i})
			i += 2
		case c == '|':
			if i+1 >= len(src) || src[i+1] != '|' {
				return nil, fmt.Errorf("lex: stray '|' at %d (expected ||)", i)
			}
			out = append(out, token{tConcat, "||", i})
			i += 2
		case c == ':':
			if i+1 >= len(src) || src[i+1] != ':' {
				return nil, fmt.Errorf("lex: stray ':' at %d (expected ::)", i)
			}
			out = append(out, token{tCast, "::", i})
			i += 2
		default:
			tok, n, err := lexDefault(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, tok)
			i += n
		}
	}
	out = append(out, token{tEOF, "", len(src)})
	return out, nil
}

// lexDefault handles the lex cases that don't have unique two-character
// followups: single-byte punctuation (via singleByteTokens), strings,
// quoted idents, parameters, numbers, idents/keywords. Returns the
// token plus the number of source bytes consumed.
func lexDefault(src string, i int) (token, int, error) {
	c := src[i]
	if k, ok := singleByteTokens[c]; ok {
		return token{k, string(c), i}, 1, nil
	}
	switch {
	case c == '\'':
		return lexString(src, i)
	case c == '$':
		return lexParam(src, i)
	case c == '"':
		return lexQuotedIdent(src, i)
	case isDigit(c):
		tok, n := lexNumber(src, i)
		return tok, n, nil
	case isIdentStart(c):
		tok, n := lexIdent(src, i)
		return tok, n, nil
	default:
		return token{}, 0, fmt.Errorf("lex: unexpected %q at %d", c, i)
	}
}

func skipLineComment(src string, i int) int {
	for i < len(src) && src[i] != '\n' {
		i++
	}
	return i
}

func lexLt(src string, i int) (token, int) {
	if i+1 < len(src) {
		switch src[i+1] {
		case '=':
			return token{tLte, "<=", i}, 2
		case '>':
			return token{tNeq, "<>", i}, 2
		}
	}
	return token{tLt, "<", i}, 1
}

func lexGt(src string, i int) (token, int) {
	if i+1 < len(src) && src[i+1] == '=' {
		return token{tGte, ">=", i}, 2
	}
	return token{tGt, ">", i}, 1
}

func lexString(src string, i int) (token, int, error) {
	start := i
	i++ // opening quote
	var sb strings.Builder
	for i < len(src) {
		if src[i] == '\'' {
			// Doubled '' is an escaped single quote.
			if i+1 < len(src) && src[i+1] == '\'' {
				sb.WriteByte('\'')
				i += 2
				continue
			}
			return token{tString, sb.String(), start}, i - start + 1, nil
		}
		sb.WriteByte(src[i])
		i++
	}
	return token{}, 0, fmt.Errorf("lex: unterminated string at %d", start)
}

func lexParam(src string, i int) (token, int, error) {
	start := i
	i++ // $
	j := i
	for j < len(src) && isDigit(src[j]) {
		j++
	}
	if j == i {
		return token{}, 0, fmt.Errorf("lex: $ without digits at %d", start)
	}
	return token{tParam, src[i:j], start}, j - start, nil
}

func lexQuotedIdent(src string, i int) (token, int, error) {
	start := i
	i++ // opening quote
	j := i
	for j < len(src) && src[j] != '"' {
		j++
	}
	if j >= len(src) {
		return token{}, 0, fmt.Errorf("lex: unterminated quoted ident at %d", start)
	}
	return token{tIdent, src[i:j], start}, j - start + 1, nil
}

func lexNumber(src string, i int) (token, int) {
	start := i
	for i < len(src) && isDigit(src[i]) {
		i++
	}
	return token{tNumber, src[start:i], start}, i - start
}

func lexIdent(src string, i int) (token, int) {
	start := i
	for i < len(src) && isIdentCont(src[i]) {
		i++
	}
	word := src[start:i]
	lower := strings.ToLower(word)
	if k, ok := keywords[lower]; ok {
		return token{k, lower, start}, i - start
	}
	return token{tIdent, lower, start}, i - start
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c == '_' || unicode.IsLetter(rune(c)) }
func isIdentCont(c byte) bool  { return isIdentStart(c) || isDigit(c) }
