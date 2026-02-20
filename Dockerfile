# Step 1: Use the official Golang image to create a build stage
FROM --platform=$BUILDPLATFORM golang:1.26-alpine3.23 AS builder

# Step 2: Set build arguments for cross-compilation
ARG TARGETOS
ARG TARGETARCH

# Step 3: Set the Current Working Directory inside the container
WORKDIR /app

# Step 4: Copy go.mod and go.sum files to the workspace
COPY go.mod go.sum ./

# Step 5: Download all the dependencies
RUN go mod download

# Step 6: Copy the source code into the container
COPY . .

# Step 7: Build the Go app with cross-compilation support
ENV GIN_MODE=release
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o tsb-service cmd/app/main.go

# Step 7: Use base alpine image to create the final image
FROM alpine:3.23

# Step 8: Set the working directory and environment variables
WORKDIR /app

# Step 9: Copy the binary from the build container
COPY --from=builder /app/tsb-service .

# Step 10: Expose the port the app listens on
EXPOSE 8080

# Step 11: Health check
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -qO- http://localhost:8080/api/v1/up || exit 1

# Step 12: Command to run the app
ENV GIN_MODE=release
CMD ["./tsb-service"]
