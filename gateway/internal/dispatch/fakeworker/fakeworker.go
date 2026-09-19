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

	mu         sync.Mutex
	health     *workerpb.HealthResponse
	failing    bool
	probes     int
	syntheses  int
	chunkCount int
	chunkBytes int
	grpcSrv    *grpc.Server
	listener   net.Listener
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

// SetAudio sets what Synthesize streams back: chunkCount frames of chunkBytes each.
func (s *Server) SetAudio(chunkCount, chunkBytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunkCount, s.chunkBytes = chunkCount, chunkBytes
}

// Syntheses counts Synthesize calls, which is how a test proves a cache hit touched no
// worker (T-08).
func (s *Server) Syntheses() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syntheses
}

// Synthesize streams deterministic PCM so a test can assert on exact bytes.
func (s *Server) Synthesize(req *workerpb.SynthesizeRequest, stream workerpb.Worker_SynthesizeServer) error {
	s.mu.Lock()
	s.syntheses++
	count, size, failing := s.chunkCount, s.chunkBytes, s.failing
	s.mu.Unlock()

	if failing {
		return status.Error(codes.Unavailable, "fake worker is failing")
	}
	if count == 0 {
		count, size = 2, 480
	}

	rate := req.GetOutputSampleRate()
	if rate == 0 {
		rate = 48000
	}

	var seq uint32
	//nolint:gosec // count is a small frame count set by the test that owns this server
	for ; seq < uint32(count); seq++ {
		frame := make([]byte, size)
		for i := range frame {
			frame[i] = byte(seq + 1)
		}
		if err := stream.Send(&workerpb.AudioFrame{
			Seq: seq, Pcm16: frame, SampleRate: rate,
		}); err != nil {
			return fmt.Errorf("send frame: %w", err)
		}
	}

	if err := stream.Send(&workerpb.AudioFrame{
		Seq: seq, SampleRate: rate, Last: true,
		//nolint:gosec // text length is bounded by the contract's 3,000 character limit
		CharsConsumed: uint32(len([]rune(req.GetText()))),
	}); err != nil {
		return fmt.Errorf("send terminator: %w", err)
	}
	return nil
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
