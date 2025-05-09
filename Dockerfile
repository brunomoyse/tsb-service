# Step 1: Use the official Golang image to create a build stage
FROM golang:1.24-alpine3.21 AS builder

# Step 2: Set the Current Working Directory inside the container
WORKDIR /app

# Step 3: Copy go.mod and go.sum files to the workspace
COPY go.mod go.sum ./

# Step 4: Download all the dependencies
RUN go mod download

# Step 5: Copy the source code into the container
COPY . .

# Step 6: Build the Go app
ENV GIN_MODE=release
RUN go build -o tsb-service cmd/app/main.go

# Step 7: Use base alpine image to create the final image
FROM alpine:latest

# Step 8: Set the working directory and environment variables
WORKDIR /app

# Step 9: Copy the binary from the build container
COPY --from=builder /app/tsb-service .

# Step 10: Expose the port the app listens on
EXPOSE 8080

# Step 11: Command to run the app
ENV GIN_MODE=release
CMD ["./tsb-service"]
