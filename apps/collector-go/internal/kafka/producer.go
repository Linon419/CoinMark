package kafka

import (
	"fmt"

	"github.com/IBM/sarama"
)

type Producer struct {
	topic string
	p     sarama.SyncProducer
}

func NewProducer(brokers []string, clientID string, topic string) (*Producer, error) {
	conf := sarama.NewConfig()
	conf.ClientID = clientID
	conf.Version = sarama.V2_0_0_0
	conf.Producer.RequiredAcks = sarama.WaitForLocal
	conf.Producer.Retry.Max = 5
	conf.Producer.Return.Successes = true

	p, err := sarama.NewSyncProducer(brokers, conf)
	if err != nil {
		return nil, fmt.Errorf("new sync producer: %w", err)
	}

	return &Producer{topic: topic, p: p}, nil
}

func (p *Producer) Send(key []byte, value []byte) error {
	msg := &sarama.ProducerMessage{
		Topic: p.topic,
		Key:   sarama.ByteEncoder(key),
		Value: sarama.ByteEncoder(value),
	}
	_, _, err := p.p.SendMessage(msg)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

func (p *Producer) Close() error {
	if p == nil || p.p == nil {
		return nil
	}
	return p.p.Close()
}
