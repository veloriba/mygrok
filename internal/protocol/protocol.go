package protocol

const (
	DefaultControlPort = "7000"
	DefaultHTTPPort    = "8080"
)

type HandshakeRequest struct {
	Token     string `json:"token"`
	Subdomain string `json:"subdomain"` // Requested subdomain, empty for random
	Protocol  string `json:"protocol"`  // "http" for now
}

type HandshakeResponse struct {
	Status    string `json:"status"` // "ok" or "error"
	Message   string `json:"message,omitempty"`
	Subdomain string `json:"subdomain,omitempty"`
	URL       string `json:"url,omitempty"`
}
