#!/bin/sh

p=`pwd`

export GOPATH=$p/lib/
export GIT_SSL_NO_VERIFY=1

echo "get libs"
#go get -u -d github.com/mikeflynn/go-alexa/skillserver
go get -u -d gitlab.com/waringer/go-alexa/skillserver
go get -u -d github.com/codegangsta/negroni
go get -u -d github.com/mmcdole/gofeed

echo "build wetterochs"
go build -ldflags "-X main.buildstamp=`date -u '+%Y-%m-%d_%I:%M:%S%p'` -X main.githash=`git rev-parse HEAD`" wetterochs.go

