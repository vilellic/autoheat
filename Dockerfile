FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /autoheat ./cmd/autoheat

# The binary is static and embeds time zone data, so nothing else is needed.
FROM scratch
COPY --from=build /autoheat /autoheat
ENV TZ=Europe/Helsinki
EXPOSE 8080
ENTRYPOINT ["/autoheat", "-config", "/config/config.yaml"]
