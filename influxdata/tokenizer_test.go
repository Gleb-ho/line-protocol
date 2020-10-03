package influxdata

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	qt "github.com/frankban/quicktest"
)

var tokenizerTakeTests = []struct {
	testName     string
	newTokenizer func(s string) *Tokenizer
	expectError  string
}{{
	testName: "bytes",
	newTokenizer: func(s string) *Tokenizer {
		return NewTokenizerWithBytes([]byte(s))
	},
}, {
	testName: "reader",
	newTokenizer: func(s string) *Tokenizer {
		return NewTokenizer(strings.NewReader(s))
	},
}, {
	testName: "one-byte-reader",
	newTokenizer: func(s string) *Tokenizer {
		return NewTokenizer(iotest.OneByteReader(strings.NewReader(s)))
	},
}, {
	testName: "data-err-reader",
	newTokenizer: func(s string) *Tokenizer {
		return NewTokenizer(iotest.DataErrReader(strings.NewReader(s)))
	},
}, {
	testName: "error-reader",
	newTokenizer: func(s string) *Tokenizer {
		return NewTokenizer(&errorReader{
			r:   strings.NewReader(s),
			err: fmt.Errorf("some error"),
		})
	},
	expectError: "some error",
}}

// TestTokenizerTake tests the internal Tokenizer.take method.
func TestTokenizerTake(t *testing.T) {
	c := qt.New(t)
	for _, test := range tokenizerTakeTests {
		c.Run(test.testName, func(c *qt.C) {
			s := "aabbcccddefga"
			tok := test.newTokenizer(s)
			data1 := tok.take(newByteSet('a', 'b', 'c'))
			c.Assert(string(data1), qt.Equals, "aabbccc")

			data2 := tok.take(newByteSet('d'))
			c.Assert(string(data2), qt.Equals, "dd")

			data3 := tok.take(newByteSet(' ').invert())
			c.Assert(string(data3), qt.Equals, "efga")
			c.Assert(tok.complete, qt.Equals, true)

			data4 := tok.take(newByteSet(' ').invert())
			c.Assert(string(data4), qt.Equals, "")

			// Check that none of them have been overwritten.
			c.Assert(string(data1), qt.Equals, "aabbccc")
			c.Assert(string(data2), qt.Equals, "dd")
			c.Assert(string(data3), qt.Equals, "efga")
			if test.expectError != "" {
				c.Assert(tok.err, qt.ErrorMatches, test.expectError)
			} else {
				c.Assert(tok.err, qt.IsNil)
			}
		})
	}
}

func TestLongTake(t *testing.T) {
	c := qt.New(t)
	// Test that we can take segments that are longer than the
	// read buffer size.
	src := strings.Repeat("abcdefgh", (minRead*3)/8)
	tok := NewTokenizer(strings.NewReader(src))
	data := tok.take(newByteSet('a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'))
	c.Assert(string(data), qt.Equals, src)
}

func TestTakeWithReset(t *testing.T) {
	c := qt.New(t)
	// Test that we can take segments that are longer than the
	// read buffer size.
	lineCount := (minRead * 3) / 9
	src := strings.Repeat("abcdefgh\n", lineCount)
	tok := NewTokenizer(strings.NewReader(src))
	n := 0
	for {
		data := tok.take(newByteSet('a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'))
		if len(data) == 0 {
			break
		}
		n++
		c.Assert(string(data), qt.Equals, "abcdefgh")
		b := tok.at(0)
		c.Assert(b, qt.Equals, byte('\n'))
		tok.advance(1)
		tok.reset()
	}
	c.Assert(n, qt.Equals, lineCount)
}

func TestTokenizerTakeWithReset(t *testing.T) {
	c := qt.New(t)
	// With a byte-at-a-time reader, we won't read any more
	// than we absolutely need.
	tok := NewTokenizer(iotest.OneByteReader(strings.NewReader("aabbcccddefg")))
	data1 := tok.take(newByteSet('a', 'b', 'c'))
	c.Assert(string(data1), qt.Equals, "aabbccc")
	c.Assert(tok.at(0), qt.Equals, byte('d'))
	tok.advance(1)
	tok.reset()
	c.Assert(tok.r0, qt.Equals, 0)
	c.Assert(tok.r1, qt.Equals, 0)
}

func TestTokenizerTakeEsc(t *testing.T) {
	c := qt.New(t)
	for _, test := range tokenizerTakeTests {
		c.Run(test.testName, func(c *qt.C) {
			tok := test.newTokenizer(`hello\ \t\\z\XY`)
			data := tok.takeEsc(newByteSet('X').invert(), &newEscaper(" \t").revTable)
			c.Assert(string(data), qt.Equals, "hello \t\\\\z\\")

			// Check that an escaped character will be included when
			// it's not part of the take set.
			tok = test.newTokenizer(`hello\ \t\\z\XYX`)
			data1 := tok.takeEsc(newByteSet('X').invert(), &newEscaper("X \t").revTable)
			c.Assert(string(data1), qt.Equals, "hello \t\\\\zXY")

			// Check that the next call to takeEsc continues where it left off.
			data2 := tok.takeEsc(newByteSet(' ').invert(), &newEscaper(" ").revTable)
			c.Assert(string(data2), qt.Equals, "X")
			// Check that data1 hasn't been overwritten.
			c.Assert(string(data1), qt.Equals, "hello \t\\\\zXY")

			// Check that a backslash followed by EOF is taken as literal.
			tok = test.newTokenizer(`x\`)
			data = tok.takeEsc(newByteSet().invert(), &newEscaper(" ").revTable)
			c.Assert(string(data), qt.Equals, "x\\")
		})
	}
}

func TestTokenizerTakeEscSkipping(t *testing.T) {
	c := qt.New(t)
	tok := NewTokenizer(strings.NewReader(`hello\ \t\\z\XY`))
	tok.skipping = true
	data := tok.takeEsc(newByteSet('X').invert(), &newEscaper(" \t").revTable)
	// When skipping is true, the data isn't unquoted (that's just unnecessary extra work).
	c.Assert(string(data), qt.Equals, `hello\ \t\\z\`)
}

type errorReader struct {
	r   io.Reader
	err error
}

func (r *errorReader) Read(buf []byte) (int, error) {
	n, err := r.r.Read(buf)
	if err != nil {
		err = r.err
	}
	return n, err
}

func BenchmarkTokenize(b *testing.B) {
	var buf bytes.Buffer
	b.ReportAllocs()
	total := 0
	for buf.Len() < 25*1024*1024 {
		buf.WriteString("foo ba\\ rfle ")
		for i := 0; i < 5000; i += 5 {
			buf.WriteString("abcde")
		}
		buf.WriteByte('\n')
		total++
	}
	b.SetBytes(int64(buf.Len()))
	esc := newEscaper(" \t")
	whitespace := newByteSet(' ', '\t')
	word := newByteSet(' ', '\t', '\n').invert()
	tokBytes := buf.Bytes()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		tok := NewTokenizerWithBytes(tokBytes)
		for {
			tok.reset()
			if !tok.ensure(1) {
				break
			}
			tok.takeEsc(word, &esc.revTable)
			tok.take(whitespace)
			if !tok.ensure(1) {
				break
			}
			tok.takeEsc(word, &esc.revTable)
			tok.take(whitespace)
			if !tok.ensure(1) {
				break
			}
			tok.takeEsc(word, &esc.revTable)
			tok.take(whitespace)
			if !tok.ensure(1) {
				break
			}
			if tok.at(0) != '\n' {
				b.Fatalf("unexpected character %q at eol", string(rune(tok.at(0))))
			}
			tok.advance(1)
			n++
		}
		if n != total {
			b.Fatalf("unexpected read count; got %v want %v", n, total)
		}
	}
}
