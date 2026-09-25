package api

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"
)

// DefaultHealthInterval is how often upstream ports are checked.
const DefaultHealthInterval = 5 * time.Second

const dialTimeout = time.Second

// checker dials each tracked upstream port on an interval and reports changes.
type checker struct {
	interval time.Duration
	onChange func(port int, h Health)

	mu    sync.Mutex
	state map[int]Health
	kick  chan struct{}
}

func newChecker(interval time.Duration, onChange func(int, Health)) *checker {
	return &checker{interval: interval, onChange: onChange, state: map[int]Health{}, kick: make(chan struct{}, 1)}
}

// track replaces the set of ports; new ports start unknown and are checked soon.
func (c *checker) track(ports []int) {
	c.mu.Lock()
	next := make(map[int]Health, len(ports))
	for _, p := range ports {
		if h, ok := c.state[p]; ok {
			next[p] = h
		} else {
			next[p] = HealthUnknown
		}
	}
	c.state = next
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *checker) health(port int) Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.state[port]; ok {
		return h
	}
	return HealthUnknown
}

func (c *checker) run(ctx context.Context) {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		c.checkAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.kick:
		}
	}
}

func (c *checker) checkAll(ctx context.Context) {
	c.mu.Lock()
	ports := make([]int, 0, len(c.state))
	for p := range c.state {
		ports = append(ports, p)
	}
	c.mu.Unlock()

	var wg sync.WaitGroup
	for _, p := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := dial(ctx, p)
			if ctx.Err() != nil {
				return // shutting down; not a real failure
			}
			c.mu.Lock()
			old, tracked := c.state[p]
			if tracked {
				c.state[p] = h
			}
			c.mu.Unlock()
			if tracked && old != h {
				c.onChange(p, h)
			}
		}()
	}
	wg.Wait()
}

func dial(ctx context.Context, port int) Health {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return HealthDown
	}
	_ = conn.Close()
	return HealthUp
}
