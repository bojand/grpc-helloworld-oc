```sh
docker run \
    -p 9090:9090 \
    -v /Users/bdjurkovic/go/grpc-helloworld-oc/prometheus.yaml:/etc/prometheus/prometheus.yml \
    prom/prometheus
```