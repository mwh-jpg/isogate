# 多阶段构建：编译期用 golang 镜像，运行期用 distroless/alpine 缩小攻击面
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/server ./server
COPY migrations ./migrations
# 前端由 docker-compose 挂载到 ./frontend（见 deploy/docker-compose.full.yml），
# 或将 frontend/ 复制到 backend/ 下一起构建。
USER nobody
EXPOSE 8080
ENTRYPOINT ["./server"]
