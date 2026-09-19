// Package fakeworker is an in-process gRPC worker for tests (.claude/rules/gateway-go.md).
// It implements the real contract, so dispatch tests exercise real gRPC rather than a
// hand-rolled stub of the client interface.
package fakeworker

import (
	"context"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
)

// Server is a controllable worker: tests set what Health reports and how it fails.
type Server struct {
	workerpb.UnimplementedWorkerServer

	mu       sync.Mutex
	health   *workerpb.HealthResponse
	failing  bool
	probes   int
	grpcSrv  *grpc.Server
	listener net.Listener
}

// Start listens on a loopback port and serves until Stop.
func Start() (*Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	s := &Server{
		listener: listener,
		grpcSrv:  grpc.NewServer(),
		health: &workerpb.HealthResponse{
			Ready: true, ModelVersion: "test", SlotsTotal: 1, SlotsBusy: 0,
		},
	}
	workerpb.RegisterWorkerServer(s.grpcSrv, s)

	go func() { _ = s.grpcSrv.Serve(listener) }()
	return s, nil
}

// Addr is the address to give the pool.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// SetHealth replaces what Health reports.
func (s *Server) SetHealth(h *workerpb.HealthResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health = h
}

// SetFailing makes Health return an error, simulating a sick worker.
func (s *Server) SetFailing(failing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = failing
}

// Probes counts Health calls received.
func (s *Server) Probes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probes
}

// Health implements the worker contract.
func (s *Server) Health(context.Context, *workerpb.HealthRequest) (*workerpb.HealthResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.probes++
	if s.failing {
		return nil, status.Error(codes.Unavailable, "fake worker is failing")
	}
	return s.health, nil
}

// Stop shuts the server down.
func (s *Server) Stop() { s.grpcSrv.Stop() }
