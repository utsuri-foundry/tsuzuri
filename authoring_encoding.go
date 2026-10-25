package tsuzuri

import (
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"strings"
)

// Existing pending creation receipts contain a rendered content digest. Matching
// their bytes requires the released writer's string spelling, not merely TOML
// semantic equivalence. The TOML encoder still owns tables, arrays and types.
func releasedStringSpelling(encoded string) (string, error) {
	var out strings.Builder
	header := false
	for i := 0; i < len(encoded); {
		c := encoded[i]
		if c == '\n' {
			header = false
		}
		if c == '[' && (i == 0 || encoded[i-1] == '\n') {
			header = true
		}
		if c != '\'' && c != '"' {
			out.WriteByte(c)
			i++
			continue
		}
		start := i
		i++
		for i < len(encoded) {
			if c == '"' && encoded[i] == '\\' {
				i += 2
				continue
			}
			if encoded[i] == c {
				i++
				break
			}
			i++
		}
		if i > len(encoded) {
			return "", invalid("invalid encoded TOML string")
		}
		token := encoded[start:i]
		var value struct {
			V string `toml:"v"`
		}
		if e := toml.Unmarshal([]byte("v = "+token), &value); e != nil {
			return "", invalid("cannot normalize encoded TOML string: %v", e).causedBy(e)
		}
		after := strings.TrimLeft(encoded[i:], " \t")
		key := header || strings.HasPrefix(after, "=") || strings.HasPrefix(after, ".")
		out.WriteString(releasedQuote(value.V, key))
	}
	return out.String(), nil
}
func releasedQuote(s string, key bool) string {
	newline, controls, escape := false, false, false
	single, double, maxSingle, maxDouble := 0, 0, 0, 0
	for _, c := range []byte(s) {
		if c == '\'' {
			single++
		} else {
			single = 0
		}
		if c == '"' {
			double++
		} else {
			double = 0
		}
		if single > maxSingle {
			maxSingle = single
		}
		if double > maxDouble {
			maxDouble = double
		}
		if c == '\n' {
			newline = true
		} else if c != '\t' && (c < 32 || c == 127) {
			controls = true
		}
		if c == '\\' {
			escape = true
		}
	}
	literal, multiline := false, false
	switch {
	case !controls && !escape && maxDouble == 0 && (!newline || key):
		// Basic string, with escaped newlines in keys.
	case !controls && maxSingle == 0 && !newline:
		literal = true
	case !key && !controls && !escape && maxDouble <= 2:
		multiline = true
	case !key && !controls && maxSingle <= 2:
		literal = true
		multiline = true
	default:
		multiline = newline && !key
	}
	delimiter := "\""
	if literal {
		delimiter = "'"
	}
	if multiline {
		delimiter = strings.Repeat(delimiter, 3)
	}
	var out strings.Builder
	out.WriteString(delimiter)
	if multiline && newline {
		out.WriteByte('\n')
	}
	if literal {
		out.WriteString(s)
	} else {
		quoteRun := 0
		for _, c := range []byte(s) {
			if c == '"' {
				quoteRun++
			} else {
				quoteRun = 0
			}
			switch c {
			case '\b':
				out.WriteString(`\b`)
			case '\t':
				out.WriteString(`\t`)
			case '\n':
				if multiline {
					out.WriteByte(c)
				} else {
					out.WriteString(`\n`)
				}
			case '\f':
				out.WriteString(`\f`)
			case '\r':
				out.WriteString(`\r`)
			case '\\':
				out.WriteString(`\\`)
			case '"':
				if !multiline || quoteRun > 2 {
					out.WriteString(`\"`)
					quoteRun = 0
				} else {
					out.WriteByte(c)
				}
			default:
				if c < 32 || c == 127 {
					fmt.Fprintf(&out, `\u%04X`, c)
				} else {
					out.WriteByte(c)
				}
			}
		}
	}
	out.WriteString(delimiter)
	return out.String()
}
