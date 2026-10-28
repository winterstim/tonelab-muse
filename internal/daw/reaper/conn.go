package reaper

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Conn is the UDP link to REAPER: we send to its listen port and receive
// its feedback on ours. Every send passes the allowlist.
type Conn struct {
	pc     net.PacketConn
	reaper net.Addr

	mu   sync.Mutex
	subs map[int]sub
	next int
	last time.Time // when feedback last arrived
}

type sub struct {
	prefix string
	ch     chan Msg
}

// Dial listens on listen (e.g. "127.0.0.1:9000") and sends to reaper
// (e.g. "127.0.0.1:8000").
func Dial(listen, reaper string) (*Conn, error) {
	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", listen, err)
	}
	ra, err := net.ResolveUDPAddr("udp", reaper)
	if err != nil {
		pc.Close()
		return nil, err
	}
	c := &Conn{pc: pc, reaper: ra, subs: map[int]sub{}}
	go c.read()
	return c, nil
}

func (c *Conn) Close() error { return c.pc.Close() }

// Send encodes and sends one message if its address is allowed.
func (c *Conn) Send(addr string, args ...any) error {
	if !Allowed(addr) {
		return fmt.Errorf("%w: %s", core.ErrNotAllowed, addr)
	}
	b, err := Encode(Msg{Addr: addr, Args: args})
	if err != nil {
		return err
	}
	_, err = c.pc.WriteTo(b, c.reaper)
	return err
}

// Subscribe delivers every incoming message whose address starts with
// prefix until cancel is called. Slow readers drop messages, never block.
func (c *Conn) Subscribe(prefix string) (<-chan Msg, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.next
	c.next++
	ch := make(chan Msg, 4096)
	c.subs[id] = sub{prefix, ch}
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
	}
}

// WaitQuiet returns once no feedback has arrived for quiet, or after max.
func (c *Conn) WaitQuiet(ctx context.Context, quiet, max time.Duration) error {
	start := time.Now()
	t := time.NewTicker(quiet / 5)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-t.C:
			c.mu.Lock()
			last := c.last
			c.mu.Unlock()
			if now.Sub(last) >= quiet && now.Sub(start) >= quiet || now.Sub(start) >= max {
				return nil
			}
		}
	}
}

func (c *Conn) read() {
	buf := make([]byte, 65536)
	for {
		n, _, err := c.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		ms, err := Decode(buf[:n])
		if err != nil {
			continue
		}
		c.mu.Lock()
		c.last = time.Now()
		for _, m := range ms {
			for _, s := range c.subs {
				if strings.HasPrefix(m.Addr, s.prefix) {
					select {
					case s.ch <- m:
					default:
					}
				}
			}
		}
		c.mu.Unlock()
	}
}
