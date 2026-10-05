package client

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// managedTunnel pairs a tunnel client with its lifecycle channels: stop is
// closed to ask the reconnect loop to exit, done is closed once it has.
type managedTunnel struct {
	client *TunnelClient
	stop   chan struct{}
	done   chan struct{}
}

// TunnelManager runs a set of TunnelClients in one process: each client owns
// its own connection and reconnect loop, and in TUI mode one shared dashboard
// renders every tunnel. Reload swaps the tunnel set in place (hot reload),
// leaving unchanged tunnels untouched.
type TunnelManager struct {
	mu       sync.Mutex
	tunnels  []*managedTunnel
	noTUI    bool
	global   chan struct{}
	quitOnce sync.Once
	stopping bool
}

// NewManager validates the client list and returns the manager. Duplicate
// non-empty subdomains are rejected: the server closes the older tunnel when
// a new one takes over a subdomain, so a client that registers the same
// subdomain twice would kill its own first tunnel.
func NewManager(clients []*TunnelClient, noTUI bool) (*TunnelManager, error) {
	if len(clients) == 0 {
		return nil, errors.New("no tunnels to start")
	}
	if err := validateSubdomains(clients); err != nil {
		return nil, err
	}
	tunnels := make([]*managedTunnel, len(clients))
	for i, c := range clients {
		tunnels[i] = &managedTunnel{client: c}
	}
	return &TunnelManager{tunnels: tunnels, noTUI: noTUI, global: make(chan struct{})}, nil
}

func validateSubdomains(clients []*TunnelClient) error {
	seen := make(map[string]int, len(clients))
	for i, c := range clients {
		if c.Subdomain == "" {
			continue
		}
		if other, dup := seen[c.Subdomain]; dup {
			return fmt.Errorf("duplicate subdomain %q (tunnels %d and %d); the server closes the older tunnel on takeover", c.Subdomain, other+1, i+1)
		}
		seen[c.Subdomain] = i
	}
	return nil
}

// tunnelKey identifies a tunnel across reloads: protocol + subdomain + local
// target. A change to any of these means a different tunnel, not an edit.
func tunnelKey(c *TunnelClient) string {
	return strings.Join([]string{c.Protocol, c.Subdomain, c.LocalAddr}, "\x00")
}

// tunnelFingerprint covers every remaining field that changes runtime
// behaviour; a key match with a different fingerprint restarts the tunnel.
// The display Name is intentionally excluded: renaming is cosmetic.
func tunnelFingerprint(c *TunnelClient) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%v\x00%s\x00%s\x00%s",
		c.ServerAddr, c.Token, c.RequestedPort, c.Insecure,
		strings.Join(c.SetHeaders, ","), c.FlushInterval, c.UpstreamTimeout)
}

// ReloadReport summarizes one hot reload.
type ReloadReport struct {
	Added    int
	Removed  int
	Replaced int
	Kept     int
}

func (r ReloadReport) String() string {
	return fmt.Sprintf("%d added, %d removed, %d replaced, %d kept", r.Added, r.Removed, r.Replaced, r.Kept)
}

// Reload applies a new tunnel set without touching unchanged tunnels:
// tunnels whose key (protocol+subdomain+local target) and fingerprint match
// keep running with their existing connection; tunnels whose fingerprint
// changed are stopped and restarted; new keys start, missing keys stop.
// The new list is validated first (duplicates, empty) and a rejected reload
// changes nothing. Stopped tunnels exit on their own: the reconnect loop
// checks stop before every connect, so a replaced tunnel never races the
// replacement for the subdomain.
func (m *TunnelManager) Reload(clients []*TunnelClient) (ReloadReport, error) {
	var rep ReloadReport
	if len(clients) == 0 {
		return rep, errors.New("refusing to reload with zero tunnels")
	}
	if err := validateSubdomains(clients); err != nil {
		return rep, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return rep, errors.New("manager is shutting down")
	}

	current := make(map[string]*managedTunnel, len(m.tunnels))
	for _, t := range m.tunnels {
		current[tunnelKey(t.client)] = t
	}

	next := make([]*managedTunnel, 0, len(clients))
	for _, c := range clients {
		key := tunnelKey(c)
		if old, ok := current[key]; ok {
			if tunnelFingerprint(old.client) == tunnelFingerprint(c) {
				rep.Kept++
				next = append(next, old)
				delete(current, key)
				continue
			}
			rep.Replaced++
			m.stopTunnel(old)
			delete(current, key)
		} else {
			rep.Added++
		}
		t := &managedTunnel{client: c}
		m.startTunnel(t)
		next = append(next, t)
	}
	for _, old := range current {
		rep.Removed++
		m.stopTunnel(old)
	}
	m.tunnels = next
	return rep, nil
}

// startTunnel spawns the reconnect loop for a tunnel; caller holds m.mu.
func (m *TunnelManager) startTunnel(t *managedTunnel) {
	t.stop = make(chan struct{})
	t.done = make(chan struct{})
	go func() {
		t.client.Start(t.stop)
		close(t.done)
	}()
}

// stopTunnel asks a running tunnel to exit; caller holds m.mu. Not every
// tunnel is running yet (Reload may arrive before Start), hence the nil check.
func (m *TunnelManager) stopTunnel(t *managedTunnel) {
	if t.stop == nil {
		return
	}
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
}

// snapshots returns a point-in-time view of every tunnel for the dashboard.
func (m *TunnelManager) snapshots() []ClientSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snaps := make([]ClientSnapshot, len(m.tunnels))
	for i, t := range m.tunnels {
		snaps[i] = t.client.Snapshot()
	}
	return snaps
}

// Quit shuts the whole process down: signal handler or TUI q/ctrl-c.
func (m *TunnelManager) Quit() {
	m.quitOnce.Do(func() { close(m.global) })
}

// Start runs every tunnel until the process should shut down: either sig is
// closed (SIGINT/SIGTERM) or the user quits from the TUI dashboard (q or
// ctrl-c on unix). It returns after all tunnel loops have exited.
func (m *TunnelManager) Start(sig <-chan struct{}) {
	if sig != nil {
		go func() {
			<-sig
			m.Quit()
		}()
	}
	if !m.noTUI {
		go m.uiLoop(m.global, m.Quit)
	}

	m.mu.Lock()
	for _, t := range m.tunnels {
		m.startTunnel(t)
	}
	m.mu.Unlock()

	<-m.global

	m.mu.Lock()
	m.stopping = true
	var dones []chan struct{}
	for _, t := range m.tunnels {
		m.stopTunnel(t)
		if t.done != nil {
			dones = append(dones, t.done)
		}
	}
	m.mu.Unlock()
	for _, d := range dones {
		<-d
	}
}
