package main

import (
	"tcp_server_hot_reload/server"
)

func main() {
	srv := server.NewMgr()
	srv.Run()
}
