package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/jordanhubbard/tokenhub/orchestrator"
	"github.com/jordanhubbard/tokenhub/providers"
	"github.com/jordanhubbard/tokenhub/router"
)

// Server is the HTTP server for Tokenhub
type Server struct {
	router       *router.Router
	orchestrator *orchestrator.Orchestrator
	port         int
	host         string
}

// NewServer creates a new HTTP server
func NewServer(router *router.Router, orchestrator *orchestrator.Orchestrator, host string, port int) *Server {
	return &Server{
		router:       router,
		orchestrator: orchestrator,
		port:         port,
		host:         host,
	}
}

// Start starts the HTTP server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/completions", s.handleCompletions)
	mux.HandleFunc("/v1/adversarial", s.handleAdversarial)
	mux.HandleFunc("/health", s.handleHealth)

	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	log.Printf("Starting Tokenhub server on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req providers.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.router.RouteChatRequest(r.Context(), &req)
	if err != nil {
		log.Printf("Error routing chat request: %v", err)
		http.Error(w, fmt.Sprintf("Request failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req providers.CompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.router.RouteCompletionRequest(r.Context(), &req)
	if err != nil {
		log.Printf("Error routing completion request: %v", err)
		http.Error(w, fmt.Sprintf("Request failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleAdversarial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	result, err := s.orchestrator.AdversarialMode(r.Context(), req.Prompt)
	if err != nil {
		log.Printf("Error in adversarial mode: %v", err)
		http.Error(w, fmt.Sprintf("Request failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
	})
}
