package protocol

import (
	"encoding/binary"
	"io"
)

const (
	DefaultControlPort = "7000"
	DefaultHTTPPort    = "8080"
)

// WriteUDPPacket writes a length-prefixed UDP frame:
// [2-byte peerLen][peerAddr][2-byte payloadLen][payload].
func WriteUDPPacket(w io.Writer, peer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint16(hdr[0:2], uint16(len(peer)))
	binary.BigEndian.PutUint16(hdr[2:4], uint16(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(peer); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadUDPPacket reads a frame written by WriteUDPPacket.
func ReadUDPPacket(r io.Reader) (peer, payload []byte, err error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, nil, err
	}
	peer = make([]byte, int(binary.BigEndian.Uint16(hdr[0:2])))
	if _, err := io.ReadFull(r, peer); err != nil {
		return nil, nil, err
	}
	payload = make([]byte, int(binary.BigEndian.Uint16(hdr[2:4])))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, nil, err
	}
	return peer, payload, nil
}

// Protocol identifiers for the tunnel data path.
const (
	ProtocolHTTP = "http"
	ProtocolTCP  = "tcp"
	ProtocolUDP  = "udp"
)

type HandshakeRequest struct {
	Token     string `json:"token"`
	Subdomain string `json:"subdomain"` // Requested subdomain, empty for random
	Protocol  string `json:"protocol"`  // "http", "tcp", or "udp"
	Port      int    `json:"port,omitempty"` // Requested public port (tcp/udp); 0 = auto-assign
}

type HandshakeResponse struct {
	Status    string `json:"status"` // "ok" or "error"
	Message   string `json:"message,omitempty"`
	Subdomain string `json:"subdomain,omitempty"`
	URL       string `json:"url,omitempty"`
	Port      int    `json:"port,omitempty"` // Assigned public port (tcp/udp)
}
