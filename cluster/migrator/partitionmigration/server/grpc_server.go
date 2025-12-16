package server

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rudderlabs/rudder-go-kit/config"
	proto "github.com/rudderlabs/rudder-server/proto/cluster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// NewGRPCServer creates a new GRPCServer instance
func NewGRPCServer(conf *config.Config, pms proto.PartitionMigratorServer) *GRPCServer {
	s := &GRPCServer{
		conf: conf,
		pms:  pms,
	}
	return s
}

type GRPCServer struct {
	conf *config.Config
	pms  proto.PartitionMigratorServer
	wg   sync.WaitGroup

	server *grpc.Server
}

// Start creates the gRPC server and starts listening for incoming connections
func (s *GRPCServer) Start() error {
	s.server = s.newGrpcServer()
	port := s.conf.GetIntVar(8088, 1, "PartitionMigration.Grpc.Server.Port")
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.wg.Go(func() {
		if err := s.server.Serve(lis); err != nil {
			panic(fmt.Errorf("failed to serve grpc server: %w", err))
			// TODO: panic?
		}
	})
	return nil
}

// Stop gracefully stops the gRPC server with a timeout
func (s *GRPCServer) Stop() {
	stopped := make(chan struct{})
	s.wg.Go(func() {
		s.server.GracefulStop()
		close(stopped)
	})
	select {
	case <-stopped:
		// Graceful stop completed
	case <-time.After(s.conf.GetReloadableDurationVar(10, time.Second, "PartitionMigration.Grpc.Server.StopTimeout").Load()):
		// Timeout exceeded, force stop
		s.server.Stop()
	}
	s.wg.Wait()
}

// newGrpcServer creates and configures a new gRPC server instance
func (s *GRPCServer) newGrpcServer() *grpc.Server {
	server := grpc.NewServer(
		// Keepalive enforcement policy - controls what the server requires from clients
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			// If client pings more often than every MinTime, terminate the connection
			MinTime: s.conf.GetDurationVar(5, time.Second, "PartitionMigration.Grpc.Server.Keepalive.EnforcementPolicy.MinTime"),
			// Allow client to ping even if there are no active streams
			PermitWithoutStream: s.conf.GetBoolVar(true, "PartitionMigration.Grpc.Server.Keepalive.EnforcementPolicy.PermitWithoutStream"),
		}),
		// Keepalive parameters - controls server's own keepalive behavior
		grpc.KeepaliveParams(keepalive.ServerParameters{
			// If a client is idle for MaxConnectionIdle, send a GOAWAY
			MaxConnectionIdle: s.conf.GetDurationVar(120, time.Second, "PartitionMigration.Grpc.Server.Keepalive.Parameters.MaxConnectionIdle"),
			// Ping the client if no data is received for Time duration
			Time: s.conf.GetDurationVar(20, time.Second, "PartitionMigration.Grpc.Server.Keepalive.Parameters.Time"),
			// Wait for Timeout for the ping ack before assuming the connection is dead
			Timeout: s.conf.GetDurationVar(10, time.Second, "PartitionMigration.Grpc.Server.Keepalive.Parameters.Timeout"),
		}),
	)
	proto.RegisterPartitionMigratorServer(server, s.pms)
	return server
}
