package client

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/veloriba/mygrok/internal/protocol"
	"github.com/veloriba/mygrok/internal/version"
)

// ansiSGR matches color SGR escape sequences so padded text can be measured
// by visible length instead of raw byte length.
var ansiSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

type uiColors struct {
	blue   func(...any) string
	green  func(...any) string
	yellow func(...any) string
	cyan   func(...any) string
	red    func(...any) string
	white  func(...any) string
	dim    func(...any) string
}

func newUIColors() uiColors {
	return uiColors{
		blue:   color.New(color.FgBlue).SprintFunc(),
		green:  color.New(color.FgGreen).SprintFunc(),
		yellow: color.New(color.FgYellow).SprintFunc(),
		cyan:   color.New(color.FgCyan).SprintFunc(),
		red:    color.New(color.FgRed).SprintFunc(),
		white:  color.New(color.FgWhite, color.Bold).SprintFunc(),
		dim:    color.New(color.Faint).SprintFunc(),
	}
}

func visibleLen(s string) int {
	// Count runes, not bytes: padding is about display columns, and the
	// dashboard's status dots (●/○) are multi-byte but single-width.
	return utf8.RuneCountInString(ansiSGR.ReplaceAllString(s, ""))
}

// padVisible right-pads s with spaces up to width, measured without ANSI codes.
func padVisible(s string, width int) string {
	if l := visibleLen(s); l < width {
		return s + strings.Repeat(" ", width-l)
	}
	return s
}

// rightPad left-pads s with spaces up to width, measured without ANSI codes.
func rightPad(s string, width int) string {
	if l := visibleLen(s); l < width {
		return strings.Repeat(" ", width-l) + s
	}
	return s
}

func formatKB(b uint64) string {
	return fmt.Sprintf("%.2f KB", float64(b)/1024.0)
}

// formatBytes renders a byte count with two decimals, in KB below 1 MiB and
// MB above (the multi-tunnel table scales each direction independently).
func formatBytes(b uint64) string {
	if b < 1<<20 {
		return fmt.Sprintf("%.2f KB", float64(b)/1024.0)
	}
	return fmt.Sprintf("%.2f MB", float64(b)/float64(1<<20))
}

// multiStatusW fits the widest status cell ("● connecting"); multiReqW fits
// the REQ header and keeps single digits right-aligned under it.
const (
	multiStatusW = 13
	multiReqW    = 5
)

// uiLoop redraws the dashboard for every tunnel every 5 seconds (and
// immediately after a key press) until done is closed. Keys work when stdin
// is a TTY: j/k move the selection (wrapping), r forces a redraw, q or
// ctrl-c quits (raw mode suppresses SIGINT, so the app-level quit also
// watches for ctrl-c). Without key input (Windows, piped stdin) the
// selection auto-rotates to the most interesting tunnel each tick. quit is
// invoked on q/ctrl-c; it closes the manager's done channel.
func (m *TunnelManager) uiLoop(done <-chan struct{}, quit func()) {
	c := newUIColors()
	stop, keys := startKeyReader()
	defer stop()
	auto := !keysEnabled()

	sel := 0
	n := 0
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// handleKey applies one key and reports whether the UI should quit.
	handleKey := func(k rune) bool {
		switch k {
		case 'q', 0x03: // q or ctrl-c (raw mode suppresses SIGINT)
			quit()
			return false
		case 'j':
			sel = wrapSelection(n, sel, 1)
		case 'k':
			sel = wrapSelection(n, sel, -1)
		case 'r': // force refresh: the next loop pass redraws immediately
		}
		return true
	}

	for {
		// Apply every pending key before redrawing so a burst of keys lands
		// in one frame.
	drain:
		for {
			select {
			case k := <-keys:
				if !handleKey(k) {
					return
				}
			default:
				break drain
			}
		}

		now := time.Now()
		// Re-read the tunnel set every frame: a hot reload can add, replace,
		// or remove tunnels between frames.
		snaps := m.snapshots()
		n = len(snaps)
		if sel >= n {
			sel = 0
		}
		if auto {
			sel = autoSelect(snaps)
		}

		fmt.Print("\033[H\033[2J")
		if n == 1 {
			renderSingle(os.Stdout, c, snaps[0], now)
		} else {
			renderMulti(os.Stdout, c, snaps, sel, now)
		}

		select {
		case <-done:
			return
		case k := <-keys:
			// A key arrived while the UI was blocked: apply it and redraw
			// immediately. Any further pending keys are drained on the next
			// pass through the loop.
			if !handleKey(k) {
				return
			}
		case <-ticker.C:
		}
	}
}

// wrapSelection moves cur by dir (±1) around a ring of n items.
func wrapSelection(n, cur, dir int) int {
	if n <= 0 {
		return 0
	}
	s := cur + dir
	for s < 0 {
		s += n
	}
	return s % n
}

// autoSelect picks the most interesting tunnel for the no-key-input mode:
// a tunnel with an error wins, then the one with the most recent request,
// then the one with the most traffic; otherwise index 0.
func autoSelect(snaps []ClientSnapshot) int {
	if len(snaps) == 0 {
		return 0
	}
	for i, s := range snaps {
		if s.ErrorMsg != "" {
			return i
		}
	}
	best, bestReq := 0, time.Time{}
	for i, s := range snaps {
		if len(s.Requests) == 0 {
			continue
		}
		last := s.Requests[len(s.Requests)-1].Timestamp
		if last.After(bestReq) {
			best, bestReq = i, last
		}
	}
	if !bestReq.IsZero() {
		return best
	}
	best, bestTraffic := 0, uint64(0)
	for i, s := range snaps {
		if tr := s.BytesSent + s.BytesReceived; tr > bestTraffic {
			best, bestTraffic = i, tr
		}
	}
	return best
}

// renderSingle draws the classic single-tunnel dashboard. Its output must stay
// byte-identical to the pre-manager UI for the one-tunnel case.
func renderSingle(w io.Writer, c uiColors, s ClientSnapshot, now time.Time) {
	fmt.Fprintf(w, "%s\n\n", c.white("mygrok"))

	status := c.yellow("connecting...")
	if s.PublicURL != "" {
		status = c.green("online")
	} else if s.ErrorMsg != "" {
		status = c.red("error")
	}

	fmt.Fprintf(w, "%-20s %s\n", "Session Status", status)
	if s.ErrorMsg != "" {
		fmt.Fprintf(w, "%-20s %s\n", "Error", c.red(s.ErrorMsg))
	}
	fmt.Fprintf(w, "%-20s %s\n", "Version", version.Version)
	fmt.Fprintf(w, "%-20s %s\n", "Region", "Europe")

	// Show both HTTP and HTTPS if possible, or just the one we have
	if s.PublicURL != "" {
		fmt.Fprintf(w, "%-20s %s -> %s\n", "Forwarding", c.blue(s.PublicURL), c.cyan(s.LocalAddr))
		// If it's http, also show how it would look with https (assuming Nginx is set up)
		if strings.HasPrefix(s.PublicURL, "http://") {
			httpsURL := "https://" + s.PublicURL[7:]
			fmt.Fprintf(w, "%-20s %s -> %s\n", "", c.blue(httpsURL), c.cyan(s.LocalAddr))
		}
	}

	fmt.Fprintf(w, "\n%-20s %d\n", "Total Requests", s.TotalRequests)
	fmt.Fprintf(w, "%-20s %s\n", "KB Transmitted", formatKB(s.BytesSent))
	fmt.Fprintf(w, "%-20s %s\n", "KB Received", formatKB(s.BytesReceived))
	fmt.Fprintf(w, "%-20s %s\n", "Uptime", now.Sub(s.StartTime).Truncate(time.Second).String())

	fmt.Fprintf(w, "\n%s\n", c.white("HTTP Requests"))
	fmt.Fprintf(w, "%-20s %-10s %-10s %s\n", "TIME", "METHOD", "STATUS", "PATH")
	fmt.Fprintln(w, strings.Repeat("-", 100))

	renderRequestRows(w, c, s.Requests, nil)
}

// renderRequestRows prints the TIME/METHOD/STATUS/PATH rows of reqs, most
// recent first, in the exact column layout of the single-tunnel dashboard.
// dur, when non-nil, appends a per-row suffix (the multi-tunnel detail uses
// it for the duration column).
func renderRequestRows(w io.Writer, c uiColors, reqs []RequestInfo, dur func(RequestInfo) string) {
	for i := len(reqs) - 1; i >= 0; i-- {
		req := reqs[i]
		statusStr := fmt.Sprintf("%d", req.Status)
		rawStatus := statusStr
		if req.Status >= 200 && req.Status < 300 {
			statusStr = c.green(statusStr)
		} else if req.Status >= 400 {
			statusStr = c.red(statusStr)
		}

		// Handle padding manually because ANSI codes mess up fmt.Printf width
		count := 10 - len(rawStatus)
		if count < 0 {
			count = 0
		}
		padding := strings.Repeat(" ", count)

		row := fmt.Sprintf("%-20s %-10s %s%s %s",
			req.Timestamp.Format("15:04:05.000"),
			req.Method,
			statusStr,
			padding,
			req.Path,
		)
		if dur != nil {
			row += " " + dur(req)
		}
		fmt.Fprintln(w, row)
	}
}

// multiForwarding is the plain-text forwarding cell of a table row: the
// public URL plus the local target when online, the local target alone
// otherwise.
func multiForwarding(s ClientSnapshot) string {
	if s.PublicURL != "" {
		return s.PublicURL + " -> " + s.LocalAddr
	}
	return s.LocalAddr
}

// multiTableWidths sizes the NAME and FORWARDING columns to the widest cell
// in the frame (bounded below by the header widths) so every row stays
// aligned even with long names or public URLs.
func multiTableWidths(snaps []ClientSnapshot) (nameW, fwdW int) {
	nameW, fwdW = 8, 31
	for _, s := range snaps {
		if l := visibleLen(s.Name); l > nameW {
			nameW = l
		}
		if l := visibleLen(multiForwarding(s)); l > fwdW {
			fwdW = l
		}
	}
	return
}

// renderMulti draws the multi-tunnel dashboard: a header with the version
// and tunnel counts, a one-row-per-tunnel table, and a detail section for
// the selected tunnel.
func renderMulti(w io.Writer, c uiColors, snaps []ClientSnapshot, sel int, now time.Time) {
	online, errs, connecting := 0, 0, 0
	for _, s := range snaps {
		switch s.Status {
		case "online":
			online++
		case "error":
			errs++
		default:
			connecting++
		}
	}

	fmt.Fprintf(w, "%s\n\n", c.white("mygrok"))

	left := fmt.Sprintf("mygrok v%s · %d tunnels · %d online · %d error", version.Version, len(snaps), online, errs)
	if connecting > 0 {
		left += fmt.Sprintf(" · %d connecting", connecting)
	}
	fmt.Fprintf(w, "%s     j/k select · r refresh · q quit\n", left)

	fmt.Fprintln(w)

	nameW, fwdW := multiTableWidths(snaps)
	fmt.Fprintf(w, "%s %s %s %s  %s\n",
		padVisible("NAME", nameW),
		padVisible("STATUS", multiStatusW),
		padVisible("FORWARDING", fwdW),
		rightPad("REQ", multiReqW),
		"TRAFFIC (sent/received)")

	for _, s := range snaps {
		status, colorize := "● connecting", c.yellow
		switch s.Status {
		case "online":
			status, colorize = "● online", c.green
		case "error":
			status, colorize = "○ error", c.red
		}
		fwd := c.cyan(s.LocalAddr)
		if s.PublicURL != "" {
			fwd = c.blue(s.PublicURL) + " -> " + c.cyan(s.LocalAddr)
		}
		reqs := "-"
		if s.Protocol == protocol.ProtocolHTTP {
			reqs = fmt.Sprintf("%d", s.TotalRequests)
		}
		fmt.Fprintf(w, "%s %s %s %s  %s / %s\n",
			padVisible(c.white(s.Name), nameW),
			padVisible(colorize(status), multiStatusW),
			padVisible(fwd, fwdW),
			rightPad(reqs, multiReqW),
			formatBytes(s.BytesSent),
			formatBytes(s.BytesReceived))
	}

	fmt.Fprintln(w)
	renderDetail(w, c, snaps[sel], now)
}

// renderDetail prints the selected tunnel's detail section: its last HTTP
// requests, or a small stats line for tcp/udp tunnels.
func renderDetail(w io.Writer, c uiColors, s ClientSnapshot, now time.Time) {
	if s.Protocol == protocol.ProtocolHTTP {
		fmt.Fprintf(w, "── %s · last requests ──\n", s.Name)
		if len(s.Requests) == 0 {
			fmt.Fprintln(w, c.dim("no requests yet"))
			return
		}
		renderRequestRows(w, c, s.Requests, func(r RequestInfo) string {
			return r.Duration.Truncate(time.Millisecond).String()
		})
		return
	}
	fmt.Fprintf(w, "── %s · stats ──\n", s.Name)
	fmt.Fprintf(w, "%s sent, %s received · up %s\n",
		formatBytes(s.BytesSent), formatBytes(s.BytesReceived),
		now.Sub(s.StartTime).Truncate(time.Second))
}
