// Package cache wraps the Valkey client used for the readiness check.
package cache

import (
	"context"
	"fmt"
	"net"

	valkey "github.com/valkey-io/valkey-go"
)

// Open builds a Valkey client for addr. Open attempts one dial; with
// ForceSingleClient a failed dial is tolerated and the client reconnects on
// first use, so Open fails only for an address it cannot use.
func Open(addr string) (valkey.Client, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("cache: invalid Valkey address %q: %w", addr, err)
	}
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:       []string{addr},
		ForceSingleClient: true,
	})
	// With ForceSingleClient the library returns a usable client alongside the
	// initial dial error, so only a nil client is a real failure.
	if client == nil {
		return nil, fmt.Errorf("cache: create Valkey client: %w", err)
	}
	return client, nil
}

// Ping sends PING and returns nil only when Valkey answers PONG.
func Ping(ctx context.Context, c valkey.Client) error {
	reply, err := c.Do(ctx, c.B().Ping().Build()).ToString()
	if err != nil {
		return err
	}
	if reply != "PONG" {
		return fmt.Errorf("cache: unexpected PING reply %q", reply)
	}
	return nil
}
