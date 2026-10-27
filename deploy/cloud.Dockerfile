# gs-cloud 镜像：两阶段构建，单二进制
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/gs-cloud ./cloud

FROM alpine:3.20
RUN adduser -D -u 10001 gsevo
USER gsevo
COPY --from=build /out/gs-cloud /usr/local/bin/gs-cloud
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["gs-cloud", "serve"]
