package basispoints

import (
	"encoding/json"
	"fmt"
	"strings"
)

// literalParser accepts data, never JavaScript expressions. Extensions to JSON
// are identifier keys, single-quoted strings and trailing commas. No evaluation.
type literalParser struct {
	source string
	pos    int
}

func (p *literalParser) space() {
	for p.pos < len(p.source) && strings.ContainsRune(" \r\n\t", rune(p.source[p.pos])) {
		p.pos++
	}
}
func literalIdentifierStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
func literalIdentifierByte(c byte) bool { return literalIdentifierStart(c) || c >= '0' && c <= '9' }
func (p *literalParser) quoted() (string, error) {
	quote := p.source[p.pos]
	p.pos++
	var out strings.Builder
	out.WriteByte('"')
	for p.pos < len(p.source) {
		c := p.source[p.pos]
		p.pos++
		if c == quote {
			out.WriteByte('"')
			var decoded string
			err := json.Unmarshal([]byte(out.String()), &decoded)
			return decoded, err
		}
		if c < 0x20 {
			return "", fmt.Errorf("control character in literal")
		}
		if c == '"' {
			out.WriteByte(92)
			out.WriteByte('"')
			continue
		}
		if c != 92 {
			out.WriteByte(c)
			continue
		}
		if p.pos == len(p.source) {
			break
		}
		escape := p.source[p.pos]
		p.pos++
		switch escape {
		case 39:
			out.WriteByte(39)
		case '"':
			out.WriteByte(92)
			out.WriteByte('"')
		case 92, '/', 'b', 'f', 'n', 'r', 't', 'u':
			out.WriteByte(92)
			out.WriteByte(escape)
		default:
			return "", fmt.Errorf("unsupported literal escape")
		}
	}
	return "", fmt.Errorf("unterminated literal")
}
func (p *literalParser) value(depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("literal nesting limit")
	}
	p.space()
	if p.pos == len(p.source) {
		return nil, fmt.Errorf("missing literal")
	}
	switch p.source[p.pos] {
	case '"', 39:
		return p.quoted()
	case '{':
		p.pos++
		obj := object{}
		for {
			p.space()
			if p.pos == len(p.source) {
				break
			}
			if p.source[p.pos] == '}' {
				p.pos++
				return obj, nil
			}
			var key string
			if p.source[p.pos] == '"' || p.source[p.pos] == 39 {
				var err error
				key, err = p.quoted()
				if err != nil {
					return nil, err
				}
			} else {
				start := p.pos
				if !literalIdentifierStart(p.source[p.pos]) {
					return nil, fmt.Errorf("invalid literal key")
				}
				for p.pos < len(p.source) && literalIdentifierByte(p.source[p.pos]) {
					p.pos++
				}
				key = p.source[start:p.pos]
			}
			if _, duplicate := obj[key]; duplicate || key == "__proto__" {
				return nil, fmt.Errorf("ambiguous literal key")
			}
			p.space()
			if p.pos == len(p.source) || p.source[p.pos] != ':' {
				return nil, fmt.Errorf("literal separator missing")
			}
			p.pos++
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			obj[key] = v
			p.space()
			if p.pos == len(p.source) {
				break
			}
			if p.source[p.pos] == '}' {
				p.pos++
				return obj, nil
			}
			if p.source[p.pos] != ',' {
				return nil, fmt.Errorf("literal separator missing")
			}
			p.pos++
		}
	case '[':
		p.pos++
		arr := []any{}
		for {
			p.space()
			if p.pos == len(p.source) {
				break
			}
			if p.source[p.pos] == ']' {
				p.pos++
				return arr, nil
			}
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
			p.space()
			if p.pos == len(p.source) {
				break
			}
			if p.source[p.pos] == ']' {
				p.pos++
				return arr, nil
			}
			if p.source[p.pos] != ',' {
				return nil, fmt.Errorf("literal separator missing")
			}
			p.pos++
		}
	default:
		start := p.pos
		for p.pos < len(p.source) && !strings.ContainsRune(" \r\n\t,}])", rune(p.source[p.pos])) {
			p.pos++
		}
		if start == p.pos {
			return nil, fmt.Errorf("invalid literal")
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(p.source[start:p.pos]))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if int(decoder.InputOffset()) != p.pos-start {
			return nil, fmt.Errorf("nonliteral expression")
		}
		if value == nil || value == true || value == false {
			return value, nil
		}
		if _, ok := value.(json.Number); ok {
			return value, nil
		}
		return nil, fmt.Errorf("nonliteral expression")
	}
	return nil, fmt.Errorf("incomplete literal")
}
