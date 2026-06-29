FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /keyvault-secret-reader .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /keyvault-secret-reader /keyvault-secret-reader
USER nonroot:nonroot
ENTRYPOINT ["/keyvault-secret-reader"]
