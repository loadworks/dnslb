FROM golang as build

RUN mkdir /build
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build


FROM scratch
COPY --from=build /build/dnslb /

ENTRYPOINT ["/dnslb"]
EXPOSE 8080

