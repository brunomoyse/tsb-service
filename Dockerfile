# Step 1: Use the official Golang image to create a build stage
FROM golang:1.23-bullseye AS builder

# Step 2: Set the Current Working Directory inside the container
WORKDIR /app

# Step 3: Copy go.mod and go.sum files to the workspace
COPY go.mod go.sum ./

# Step 4: Download all the dependencies
RUN go mod download

# Step 5: Copy the source code into the container
COPY . .

# Step 6: Build the Go app (replace 'tsb-service' with your actual main package if it's different)
RUN go build -o tsb-service .

# Step 7: Use an updated base image with GLIBC >= 2.34
FROM debian:bullseye-slim

# Step 8: Install CA certificates to ensure TLS connections work properly
RUN apt-get update && apt-get install -y ca-certificates && update-ca-certificates

# Step 9: Set the working directory and environment variables
WORKDIR /app

# Step 10: Copy the binary from the build container
COPY --from=builder /app/tsb-service .

# Step 11: Expose the port your app listens on
EXPOSE 8080

# Step 12: Command to run the app
CMD ["./tsb-service"]