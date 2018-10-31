#!/bin/bash

#go get -u github.com/google/pprof

docker build -f Dockerfile --network="host" -t docker_go .
docker-compose up go
docker-compose rm --force go
docker container prune -f
#docker run -it --network="bridge" -p 0.0.0.0:899:80 --rm --name go_app_test docker_go