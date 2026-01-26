ARG runtime_version
FROM golang:${runtime_version}-bookworm

# Install libstapsdt and dependencies
# Note: libstapsdt-dev may not be available in standard repos, so we build from source
RUN apt-get update && apt-get install -y \
    libelf-dev \
    git \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Build and install libstapsdt from source
RUN git clone https://github.com/sthima/libstapsdt.git /tmp/libstapsdt && \
    cd /tmp/libstapsdt && \
    make && \
    make install && \
    ldconfig && \
    rm -rf /tmp/libstapsdt

WORKDIR /app

RUN go mod init usdt

COPY app/usdt/main.go ./
COPY app/usdt/binaries/ /binaries/
COPY entry.sh ./

RUN go mod tidy
RUN CGO_ENABLED=1 go build -o main .

ENTRYPOINT ["./entry.sh"]
