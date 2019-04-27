FROM golang:1.11

COPY ./ /root
WORKDIR /root

RUN make

ENTRYPOINT ["/root/isucon6q/isucon6q-bench"]
CMD ["-debug", "-target", "http://nginx"]