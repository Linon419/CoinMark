package nats

import (
	"context"
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

type Publisher struct {
	subject string
	nc      *natsgo.Conn
	js      natsgo.JetStreamContext
}

func NewPublisher(url, clientName, streamName, subject string) (*Publisher, error) {
	if url == "" {
		return nil, fmt.Errorf("nats url is empty")
	}
	if streamName == "" {
		return nil, fmt.Errorf("nats stream name is empty")
	}
	if subject == "" {
		return nil, fmt.Errorf("nats subject is empty")
	}

	opts := []natsgo.Option{
		natsgo.Name(clientName),
		natsgo.RetryOnFailedConnect(true),
		natsgo.MaxReconnects(-1),
	}
	nc, err := natsgo.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("new jetstream context: %w", err)
	}

	if _, err := js.StreamInfo(streamName); err != nil {
		if _, addErr := js.AddStream(&natsgo.StreamConfig{
			Name:      streamName,
			Subjects:  []string{"coinmark.raw.*"},
			Retention: natsgo.LimitsPolicy,
			Storage:   natsgo.FileStorage,
			Replicas:  1,
		}); addErr != nil {
			nc.Close()
			return nil, fmt.Errorf("add stream %s: %w", streamName, addErr)
		}
	}

	return &Publisher{
		subject: subject,
		nc:      nc,
		js:      js,
	}, nil
}

func (p *Publisher) Send(ctx context.Context, payload []byte) error {
	if p == nil || p.js == nil {
		return fmt.Errorf("publisher is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := p.js.Publish(p.subject, payload, natsgo.Context(timeoutCtx))
	if err != nil {
		return fmt.Errorf("publish subject=%s: %w", p.subject, err)
	}
	return nil
}

func (p *Publisher) Close() error {
	if p == nil || p.nc == nil {
		return nil
	}
	p.nc.Close()
	return nil
}
