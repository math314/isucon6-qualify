FROM golang:1.11

COPY src /go/src
COPY ./bin/isupam_linux /go/src/isupam_linux
WORKDIR /go/src

ENV GOPATH=/go

RUN make

ENTRYPOINT ["/go/src/entry-point.sh"]