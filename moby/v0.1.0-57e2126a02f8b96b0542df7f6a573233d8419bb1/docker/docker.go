package main

import (
	"flag"
	"io"
	"log"
	"os"

	"github.com/dotcloud/docker"
	"github.com/dotcloud/docker/rcli"
	"github.com/dotcloud/docker/term"
)

// @anxk: docker的main函数，根据参数选择运行daeman或者client。
func main() {
	if docker.SelfPath() == "/sbin/init" {
		// Running in init mode
		docker.SysInit()
		return
	}
	// FIXME: Switch d and D ? (to be more sshd like)
	fl_daemon := flag.Bool("d", false, "Daemon mode")
	fl_debug := flag.Bool("D", false, "Debug mode")
	flag.Parse()
	rcli.DEBUG_FLAG = *fl_debug
	if *fl_daemon {
		if flag.NArg() != 0 {
			flag.Usage()
			return
		}
		if err := daemon(); err != nil {
			log.Fatal(err)
		}
	} else {
		if err := runCommand(flag.Args()); err != nil {
			log.Fatal(err)
		}
	}
}

// @anxk: 启动Daemon，包括实例化Server（包括Runtime的实例化），将server绑定到本地环回
// 地址，监听tcp/4242端口并准备接受来自Client的请求。
func daemon() error {
	service, err := docker.NewServer()
	if err != nil {
		return err
	}
	return rcli.ListenAndServe("tcp", "127.0.0.1:4242", service)
}

// @anxk: runCommand是客户端，负责向Server发送请求。
func runCommand(args []string) error {
	var oldState *term.State
	var err error
	if term.IsTerminal(0) && os.Getenv("NORAW") == "" {
		oldState, err = term.MakeRaw(0)
		if err != nil {
			return err
		}
		defer term.Restore(0, oldState)
	}
	// FIXME: we want to use unix sockets here, but net.UnixConn doesn't expose
	// CloseWrite(), which we need to cleanly signal that stdin is closed without
	// closing the connection.
	// See http://code.google.com/p/go/issues/detail?id=3345
	if conn, err := rcli.Call("tcp", "127.0.0.1:4242", args...); err == nil {
		receive_stdout := docker.Go(func() error {
			_, err := io.Copy(os.Stdout, conn)
			return err
		})
		send_stdin := docker.Go(func() error {
			_, err := io.Copy(conn, os.Stdin)
			if err := conn.CloseWrite(); err != nil {
				log.Printf("Couldn't send EOF: " + err.Error())
			}
			return err
		})
		if err := <-receive_stdout; err != nil {
			return err
		}
		if !term.IsTerminal(0) {
			if err := <-send_stdin; err != nil {
				return err
			}
		}
	} else {
		service, err := docker.NewServer()
		if err != nil {
			return err
		}
		if err := rcli.LocalCall(service, os.Stdin, os.Stdout, args...); err != nil {
			return err
		}
	}
	if oldState != nil {
		term.Restore(0, oldState)
	}
	return nil
}
