#!/bin/zsh

set -a
source .env
set +a

go run -race main.go