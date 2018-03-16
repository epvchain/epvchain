# Build Gepv in a stock Go builder container
FROM golang:1.9-alpine as builder

RUN apk add --no-cache make gcc musl-dev linux-headers

ADD . /go-epvchain
RUN cd /go-epvchain && make gepv

# Pull Gepv into a second stage deploy alpine container
FROM alpine:latest

RUN apk add --no-cache ca-certificates
COPY --from=builder /go-epvchain/build/bin/gepv /usr/local/bin/

EXPOSE 7545 7546 50303 50303/udp 50304/udp
ENTRYPOINT ["gepv"]
