package event

import (
	"fmt"
	"time"

	nserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Bus wraps an embedded NATS server and a connected client.
type Bus struct {
	server *nserver.Server
	conn   *nats.Conn
}

// Start launches an embedded NATS server on the given port and connects a client.
func Start(port int) (*Bus, error) {
	opts := &nserver.Options{
		Host:   "127.0.0.1",
		Port:   port,
		NoLog:  true,
		NoSigs: true,
	}

	ns, err := nserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("creating NATS server: %w", err)
	}

	go ns.Start()

	if !ns.ReadyForConnections(4 * time.Second) {
		return nil, fmt.Errorf("NATS server on port %d failed to become ready", port)
	}

	nc, err := nats.Connect(fmt.Sprintf("nats://127.0.0.1:%d", port))
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("connecting to NATS: %w", err)
	}

	return &Bus{server: ns, conn: nc}, nil
}

// Publish sends a message to the given subject.
func (b *Bus) Publish(subject string, data []byte) error {
	return b.conn.Publish(subject, data)
}

// Subscribe registers a handler for all messages on subject.
func (b *Bus) Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	return b.conn.Subscribe(subject, handler)
}

// Stop gracefully closes the client connection and shuts down the embedded server.
func (b *Bus) Stop() {
	if b.conn != nil {
		b.conn.Close()
	}
	if b.server != nil {
		b.server.Shutdown()
	}
}
