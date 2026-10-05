package client

import (
	"errors"
	"fmt"
	"sync"
)

// TunnelManager runs a set of TunnelClients in one process: each client owns
// its own connection and reconnect loop, and in TUI mode one shared dashboard
// renders every tunnel.
type TunnelManager struct {
	clients []*TunnelClient
	noTUI   bool
}

// NewManager validates the client list and returns the manager. Duplicate
// non-empty subdomains are rejected: the server closes the older tunnel when
// a new one takes over a subdomain, so a client that registers the same
// subdomain twice would kill its own first tunnel.
func NewManager(clients []*TunnelClient, noTUI bool) (*TunnelManager, error) {
	if len(clients) == 0 {
		return nil, errors.New("no tunnels to start")
	}
	seen := make(map[string]int, len(clients))
	for i, c := range clients {
		if c.Subdomain == "" {
			continue
		}
		if other, dup := seen[c.Subdomain]; dup {
			return nil, fmt.Errorf("duplicate subdomain %q (tunnels %d and %d); the server closes the older tunnel on takeover", c.Subdomain, other+1, i+1)
		}
		seen[c.Subdomain] = i
	}
	return &TunnelManager{clients: clients, noTUI: noTUI}, nil
}

// Start runs every tunnel until the process should shut down: either sig is
// closed (SIGINT/SIGTERM) or the user quits from the TUI dashboard (q or
// ctrl-c on unix). It returns after all tunnel loops have exited.
func (m *TunnelManager) Start(sig <-chan struct{}) {
	done := make(chan struct{})
	var quitOnce sync.Once
	quit := func() { quitOnce.Do(func() { close(done) }) }
	if sig != nil {
		go func() {
			<-sig
			quit()
		}()
	}
	if !m.noTUI {
		go m.uiLoop(done, quit)
	}
	var wg sync.WaitGroup
	for _, c := range m.clients {
		wg.Add(1)
		go func(c *TunnelClient) {
			defer wg.Done()
			c.Start(done)
		}(c)
	}
	wg.Wait()
}
