package event

import (
	"encoding/json"
	"io"
	"log"
	"tsb-service/internal/api/graphql/model"

	pb "tsb-service/internal/api/eventpb"
	"tsb-service/pkg/pubsub"
)

// Server implements the EventService gRPC interface
type Server struct {
	pb.UnimplementedEventServiceServer
	Broker *pubsub.Broker
}

// NewServer constructs a new event.Server
func NewServer(broker *pubsub.Broker) *Server {
	return &Server{Broker: broker}
}

// StreamEvents handles bi-directional streaming of EventMessage <-> Ack
func (s *Server) StreamEvents(stream pb.EventService_StreamEventsServer) error {
	for {
		evt, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		// Unmarshal payload into your domain model
		var rec model.TeltonikaRecord
		if err := json.Unmarshal(evt.Payload, &rec); err != nil {
			log.Printf("[telemetry] unmarshal error: %v", err)
			continue
		}

		// Publish a Position event
		pos := &model.Position{
			Longitude: rec.Longitude,
			Latitude:  rec.Latitude,
			Timestamp: rec.Timestamp,
		}

		s.Broker.Publish(rec.DeviceImei, pos)

		// Send an ACK back to the client
		if err := stream.Send(&pb.Ack{Status: "OK"}); err != nil {
			return err
		}
	}
}
