# Build stage
FROM golang:1.24 as builder

WORKDIR /src
COPY . .

# Install dependencies
RUN go mod tidy

# Build the Go app
RUN CGO_ENABLED=0 GOOS=linux go build -o multus-readiness-controller

# Run stage
FROM alpine:latest

# Install required libraries
RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the compiled binary from the build stage
COPY --from=builder /src/multus-readiness-controller .

# Command to run the controller
CMD ["./multus-readiness-controller"]
