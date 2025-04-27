package rabbit

import (
	"log"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Consumer wraps a RabbitMQ connection and channel for a single queue.
type Consumer struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	Queue   string
	C       <-chan amqp.Delivery
}

// NewConsumer dials the given AMQP URI, declares (or ensures) the queue exists,
// and starts consuming from it with auto-ack enabled.
func NewConsumer(amqpURI, queueName string) (*Consumer, error) {
	conn, err := amqp.Dial(amqpURI)
	if err != nil {
		return nil, err
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, err
	}

	if _, err := ch.QueueDeclare(
		queueName, // name
		true,      // durable
		false,     // delete when unused
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	); err != nil {
		ch.Close()
		conn.Close()
		return nil, err
	}

	deliveries, err := ch.Consume(
		queueName, // queue
		"",        // consumer tag
		true,      // auto-ack
		false,     // exclusive
		false,     // no-local
		false,     // no-wait
		nil,       // args
	)
	if err != nil {
		ch.Close()
		conn.Close()
		return nil, err
	}

	return &Consumer{
		conn:    conn,
		channel: ch,
		Queue:   queueName,
		C:       deliveries,
	}, nil
}

// Close cleanly shuts down the channel and connection.
func (c *Consumer) Close() {
	if c.channel != nil {
		_ = c.channel.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// Handle runs a handler function for each message body. This blocks.
func (c *Consumer) Handle(handler func(body []byte)) {
	log.Printf("[*] Waiting for messages on %q", c.Queue)
	for d := range c.C {
		handler(d.Body)
	}
}
