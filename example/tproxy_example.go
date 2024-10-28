package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var (
	tcpListener      net.Listener
	listenPort       int
	dontAssumeRemote bool
	noneBlock        bool
)

func init() {
	log.SetFlags(log.Lshortfile | log.Ltime)
	flag.IntVar(&listenPort, "port", 9000, "Port to listen on")
	flag.BoolVar(&dontAssumeRemote, "dont-assume-remote", false, "Don't assume remote address is the original destination")
	flag.BoolVar(&noneBlock, "none-block", false, "Don't block on read/write")
	flag.Parse()
	flag.PrintDefaults()
}

func main() {
	log.Println("Starting GoLang TProxy example listen port:"+strconv.Itoa(listenPort), "dontAssumeRemote:", dontAssumeRemote)
	var err error
	server := &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: listenPort}
	log.Println("Binding TCP TProxy listener", server)
	tcpListener, err = ListenTCP("tcp", server)
	if err != nil {
		log.Fatalln("binding listener err:", err)
		return
	}
	go listenTCP()
	interruptListener := make(chan os.Signal)
	signal.Notify(interruptListener, os.Interrupt, syscall.SIGTERM)
	<-interruptListener
	go func() {
		signal.Notify(interruptListener, os.Interrupt, syscall.SIGTERM)
		<-interruptListener
		log.Println("TProxy listener closed twice, exiting")
		os.Exit(0)
	}()
	err = tcpListener.Close()
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("TProxy listener closing")
}

func listenTCP() {
	for {
		conn, err := tcpListener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				log.Printf("Temporary error while accepting connection: %s", netErr)
			}
			log.Fatalf("Unrecoverable error while accepting connection: %s", err)
			return
		}
		go handleTCPConn(conn)
	}
}

func handleTCPConn(conn net.Conn) {
	log.Printf("Accepting TCP connection from %s with destination of %s", conn.RemoteAddr().String(), conn.LocalAddr().String())
	defer conn.Close()
	remoteConn, err := conn.(*Conn).DialOriginalDestination(dontAssumeRemote)
	if err != nil {
		log.Printf("Failed to connect to original destination [%s]: %s", conn.LocalAddr().String(), err)
		return
	}
	log.Println("client:", remoteConn.LocalAddr(), remoteConn.RemoteAddr())
	log.Println("server:", conn.LocalAddr(), conn.RemoteAddr())
	defer remoteConn.Close()
	setTimeout(conn, remoteConn)
	var streamWait sync.WaitGroup
	streamWait.Add(2)
	streamConn := func(dst *net.TCPConn, src net.Conn) {
		_, err = io.Copy(dst, src)
		if err != nil {
			log.Printf("写给server err:%s\n", err)
		}
		streamWait.Done()
		log.Println("写给server done", src.LocalAddr(), src.RemoteAddr(), dst.LocalAddr(), dst.RemoteAddr())
	}
	streamConn2 := func(dst net.Conn, src *net.TCPConn) {
		_, err = io.Copy(dst, src)
		if err != nil {
			log.Printf("写给client err:%s\n", err)
		}
		streamWait.Done()
		log.Println("写给client done", dst.LocalAddr(), dst.RemoteAddr(), src.LocalAddr(), src.RemoteAddr())
	}
	go streamConn(remoteConn, conn)
	go streamConn2(conn, remoteConn)
	streamWait.Wait()
}

func setTimeout(conn net.Conn, remoteConn *net.TCPConn) {
	// 设置读写超时时间
	timeout := 5 * time.Second // 例如10秒超时，可以根据实际需要调整
	err := conn.SetReadDeadline(time.Now().Add(timeout))
	if err != nil {
		log.Println(err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	err = remoteConn.SetReadDeadline(time.Now().Add(timeout))
	if err != nil {
		log.Println(err)
	}
	_ = remoteConn.SetWriteDeadline(time.Now().Add(timeout))
}
