package telemetry

import (
	"context"
	"encoding/json"
	"log"

	"tsb-service/internal/api/graphql/model"
	"tsb-service/pkg/pubsub"
	"tsb-service/pkg/rabbit"
)

// StartTelemetryConsumer launches your AMQP consumer loop and pushes into the broker.
// It returns when ctx is canceled.
func StartTelemetryConsumer(
	ctx context.Context,
	consumer *rabbit.Consumer,
	broker *pubsub.Broker,
) error {
	// run the RabbitMQ handler
	go consumer.Handle(func(body []byte) {
		//log.Printf("[telemetry] received %d bytes", len(body))
		//log.Println(string(body))
		var fullRec model.TeltonikaRecord
		if err := json.Unmarshal(body, &fullRec); err != nil {
			log.Printf("[telemetry] unmarshal error: %v", err)
			return
		}
		pos := &model.Position{
			Longitude: fullRec.Longitude,
			Latitude:  fullRec.Latitude,
			Timestamp: fullRec.Timestamp,
		}
		broker.Publish(fullRec.DeviceImei, pos)
	})

	// wait for shutdown
	<-ctx.Done()

	// close the consumer (no return value)
	consumer.Close()
	return nil
}
