# Step 1: Use the official Golang image to create a build stage
FROM golang:1.23 AS builder

# Step 2: Set the Current Working Directory inside the container
WORKDIR /app

# Step 3: Copy go.mod and go.sum files to the workspace
COPY go.mod go.sum ./

# Step 4: Download all the dependencies. Dependencies will be cached if go.mod and go.sum files are not changed
RUN go mod download

# Step 5: Copy the source code into the container
COPY . .

# Step 6: Build the Go app (replace 'tsb-service' with your actual main package if it's different)
RUN go build -o tsb-service .

# Step 7: Create a smaller image with just the built binary
FROM debian:buster-slim

# Step 8: Set up environment variables and configurations (if any). You can also copy the .env file here if necessary
ENV APP_ENV=production

# Step 9: Set the working directory
WORKDIR /app

# Step 10: Copy the binary from the build container to the smaller runtime container
COPY --from=builder /app/tsb-service .

# Step 11: Expose the port the app runs on (change this based on your app)
EXPOSE 8080

# Step 12: Command to run the app
CMD ["./tsb-service"]