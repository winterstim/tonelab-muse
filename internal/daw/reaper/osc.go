package reaper

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Msg is one OSC message. Args hold int32, float32 or string.
type Msg struct {
	Addr string
	Args []any
}

var errShort = errors.New("osc: packet too short")

// Encode builds the wire form of m.
func Encode(m Msg) ([]byte, error) {
	var b bytes.Buffer
	writeStr(&b, m.Addr)
	tags := []byte{','}
	var args bytes.Buffer
	for _, a := range m.Args {
		switch v := a.(type) {
		case int:
			tags = append(tags, 'i')
			binary.Write(&args, binary.BigEndian, int32(v))
		case int32:
			tags = append(tags, 'i')
			binary.Write(&args, binary.BigEndian, v)
		case float64:
			tags = append(tags, 'f')
			binary.Write(&args, binary.BigEndian, float32(v))
		case float32:
			tags = append(tags, 'f')
			binary.Write(&args, binary.BigEndian, v)
		case string:
			tags = append(tags, 's')
			writeStr(&args, v)
		default:
			return nil, fmt.Errorf("osc: unsupported arg %T", a)
		}
	}
	writeStr(&b, string(tags))
	b.Write(args.Bytes())
	return b.Bytes(), nil
}

// Decode parses a message or a bundle into its messages, in order.
func Decode(p []byte) ([]Msg, error) {
	if bytes.HasPrefix(p, []byte("#bundle\x00")) {
		if len(p) < 16 {
			return nil, errShort
		}
		var out []Msg
		for o := 16; o < len(p); {
			if o+4 > len(p) {
				return nil, errShort
			}
			n := int(binary.BigEndian.Uint32(p[o:]))
			o += 4
			if n < 0 || o+n > len(p) {
				return nil, errShort
			}
			ms, err := Decode(p[o : o+n])
			if err != nil {
				return nil, err
			}
			out = append(out, ms...)
			o += n
		}
		return out, nil
	}
	addr, o, err := readStr(p, 0)
	if err != nil {
		return nil, err
	}
	m := Msg{Addr: addr}
	if o >= len(p) { // no type tags: a bare address
		return []Msg{m}, nil
	}
	tags, o, err := readStr(p, o)
	if err != nil {
		return nil, err
	}
	for _, t := range []byte(tags)[1:] {
		switch t {
		case 'i':
			if o+4 > len(p) {
				return nil, errShort
			}
			m.Args = append(m.Args, int32(binary.BigEndian.Uint32(p[o:])))
			o += 4
		case 'f':
			if o+4 > len(p) {
				return nil, errShort
			}
			m.Args = append(m.Args, math.Float32frombits(binary.BigEndian.Uint32(p[o:])))
			o += 4
		case 's':
			var s string
			if s, o, err = readStr(p, o); err != nil {
				return nil, err
			}
			m.Args = append(m.Args, s)
		case 'T', 'F': // OSC booleans carry no payload; Live's API answers with them
			if t == 'T' {
				m.Args = append(m.Args, int32(1))
			} else {
				m.Args = append(m.Args, int32(0))
			}
		default:
			return nil, fmt.Errorf("osc: unsupported tag %q", t)
		}
	}
	return []Msg{m}, nil
}

// Float returns arg i as a float64, accepting ints too.
func (m Msg) Float(i int) (float64, bool) {
	if i >= len(m.Args) {
		return 0, false
	}
	switch v := m.Args[i].(type) {
	case float32:
		return float64(v), true
	case int32:
		return float64(v), true
	}
	return 0, false
}

// String returns arg i as a string.
func (m Msg) String(i int) (string, bool) {
	if i >= len(m.Args) {
		return "", false
	}
	s, ok := m.Args[i].(string)
	return s, ok
}

func writeStr(b *bytes.Buffer, s string) {
	b.WriteString(s)
	b.Write(make([]byte, 4-len(s)%4))
}

func readStr(p []byte, o int) (string, int, error) {
	end := bytes.IndexByte(p[o:], 0)
	if end < 0 {
		return "", 0, errShort
	}
	s := string(p[o : o+end])
	return s, o + (end+4)&^3, nil
}
