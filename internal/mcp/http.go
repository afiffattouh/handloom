package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// The HTTP half of MCP ("Streamable HTTP"): one POST per JSON-RPC message,
// answered with one JSON body. There is no event stream, so a GET is refused
// and nothing is ever held open. The hub authenticates the caller and checks
// the Origin before it hands a request here; this file only speaks the protocol.

// Paths are fixed by the connector spec; tests pin them.
const (
	// HTTPPath is the address a person pastes into their AI app.
	HTTPPath = "/mcp"
)

// Protocol versions the HTTP endpoint speaks, newest first. A client that asks
// for one of them gets it back; one that names a header version outside the
// list is refused with the list.
var SupportedProtocols = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"}

// Headers of the newest, stateless style. They are accepted, never required.
const (
	headerProtocol = "MCP-Protocol-Version"
	headerMethod   = "Mcp-Method"
)

// maxHTTPBody bounds one JSON-RPC message.
const maxHTTPBody = 256 << 10

// OperatorServer is the tool set of a person's AI app over HTTP: the same
// tools as `handloom mcp --operator`, without the approvals.
func OperatorServer(version string) *Server {
	return &Server{Version: version, Instructions: operatorInstructions, Tools: OperatorTools(false), Supported: SupportedProtocols, Probes: true}
}

func (s *Server) httpError(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{code, msg}})
}

// HandleHTTP answers one POST. The caller has already checked who is asking.
func (s *Server) HandleHTTP(w http.ResponseWriter, r *http.Request, run Run) {
	if v := r.Header.Get(headerProtocol); v != "" {
		known := false
		for _, k := range SupportedProtocols {
			known = known || k == v
		}
		if !known {
			s.httpError(w, 400, -32600, "Unsupported MCP-Protocol-Version "+clipText(v, 40)+". Supported: "+strings.Join(SupportedProtocols, ", "))
			return
		}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHTTPBody))
	if err != nil {
		s.httpError(w, 400, -32700, "the message could not be read (it may be too large)")
		return
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		s.httpError(w, 400, -32600, "send one message at a time: batches are not supported")
		return
	}
	var msg struct {
		request
		Result json.RawMessage `json:"result"`
		Err    json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || trimmed == "" {
		s.httpError(w, 400, -32700, "parse error")
		return
	}
	if msg.Method == "" {
		if len(msg.Result) > 0 || len(msg.Err) > 0 { // the client answering something: nothing to say back
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.httpError(w, 400, -32600, "not a JSON-RPC request")
		return
	}
	if m := r.Header.Get(headerMethod); m != "" && m != msg.Method {
		s.httpError(w, 400, -32001, "the Mcp-Method header ("+clipText(m, 60)+") does not match the message's method")
		return
	}
	resp := s.handle(&msg.request, run)
	if resp == nil { // a notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(resp)
}

func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
