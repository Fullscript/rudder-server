package client

import (
	"context"
	"net"
	"time"

	"github.com/rudderlabs/rudder-go-kit/config"
	proto "github.com/rudderlabs/rudder-server/proto/cluster"
	"google.golang.org/grpc"
	grpcbackoff "google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

type PartitionMigratorClient interface {
	proto.PartitionMigratorClient
	Close() error
}

func NewPartitionMigratorClient(target string, conf *config.Config) (proto.PartitionMigratorClient, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                conf.GetDurationVar(10, time.Second, "PartitionMigration.Grpc.Client.ClientParameters.Time"),
			Timeout:             conf.GetDurationVar(10, time.Second, "PartitionMigration.Grpc.Client.ClientParameters.Timeout"),
			PermitWithoutStream: conf.GetBoolVar(true, "PartitionMigration.Grpc.Client.ClientParameters.PermitWithoutStream"),
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: grpcbackoff.Config{
				BaseDelay:  conf.GetDurationVar(1, time.Second, "PartitionMigration.Grpc.Client.Backoff.BaseDelay"),
				Multiplier: conf.GetFloat64Var(1.6, "PartitionMigration.Grpc.Client.Backoff.Multiplier"),
				Jitter:     conf.GetFloat64Var(0.2, "PartitionMigration.Grpc.Client.Backoff.Jitter"),
				MaxDelay:   conf.GetDurationVar(120, time.Second, "PartitionMigration.Grpc.Client.Backoff.MaxDelay"),
			},
			MinConnectTimeout: conf.GetDurationVar(1, time.Second, "PartitionMigration.Grpc.Client.MinConnectTimeout"),
		}),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", addr)
		}))
	if err != nil {
		return nil, err
	}
	return &partitionMigrator{
		conn:                    conn,
		PartitionMigratorClient: proto.NewPartitionMigratorClient(conn),
	}, nil
}

type partitionMigrator struct {
	conn *grpc.ClientConn
	proto.PartitionMigratorClient
}

func (x *partitionMigrator) Close() error {
	return x.conn.Close()
}
