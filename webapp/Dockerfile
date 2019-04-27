FROM golang:1.11

COPY ./go /go
COPY ./bin/isupam_linux /go/isupam_linux
WORKDIR /go

RUN make

ENTRYPOINT ["/go/entry-point.sh"]