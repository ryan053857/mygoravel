#!/bin/sh
# 在一个 redis 容器里启动由三个主节点组成的集群，供集成测试使用，参数是三个端口。
# 节点向客户端报告的地址是 127.0.0.1，所以容器要把这三个端口原样映射到宿主机的 127.0.0.1。
set -eu

for port in "$@"; do
	redis-server --port "$port" --cluster-enabled yes --cluster-port $((port + 1000)) \
		--cluster-config-file "nodes-$port.conf" --cluster-announce-ip 127.0.0.1 \
		--save '' --appendonly no &
done
trap 'kill $(jobs -p) 2>/dev/null; exit 0' INT TERM

for port in "$@"; do
	until redis-cli -p "$port" ping >/dev/null 2>&1; do sleep 0.1; done
done
redis-cli --cluster create $(printf '127.0.0.1:%s ' "$@") --cluster-yes >/dev/null
wait
