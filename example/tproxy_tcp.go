//go:build linux

package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"syscall"
)

type Listener struct {
	base net.Listener
}

func (listener *Listener) Accept() (net.Conn, error) {
	return listener.AcceptTProxy()
}

func (listener *Listener) AcceptTProxy() (*Conn, error) {
	tcpConn, err := listener.base.(*net.TCPListener).AcceptTCP()
	if err != nil {
		return nil, err
	}

	return &Conn{TCPConn: tcpConn}, nil
}

func (listener *Listener) Addr() net.Addr {
	return listener.base.Addr()
}

func (listener *Listener) Close() error {
	return listener.base.Close()
}

func ListenTCP(network string, laddr *net.TCPAddr) (net.Listener, error) {
	listener, err := net.ListenTCP(network, laddr)
	if err != nil {
		return nil, err
	}

	fileDescriptorSource, err := listener.File()
	if err != nil {
		return nil, &net.OpError{Op: "listen", Net: network, Source: nil, Addr: laddr, Err: fmt.Errorf("get file descriptor: %s", err)}
	}
	defer fileDescriptorSource.Close()

	if err = syscall.SetsockoptInt(int(fileDescriptorSource.Fd()), syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
		return nil, &net.OpError{Op: "listen", Net: network, Source: nil, Addr: laddr, Err: fmt.Errorf("set socket option: IP_TRANSPARENT: %s", err)}
	}

	if err = syscall.SetsockoptInt(int(fileDescriptorSource.Fd()), syscall.SOL_SOCKET, syscall.SO_MARK, 123); err != nil {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_MARK: %s", err)}
	}

	val, getErr := syscall.GetsockoptInt(int(fileDescriptorSource.Fd()), syscall.SOL_IP, syscall.IP_TRANSPARENT)
	if getErr != nil {
		log.Fatal(getErr)
	}
	log.Printf("value of IP_TRANSPARENT option is: %d", val)

	return &Listener{listener}, nil
}

type Conn struct {
	*net.TCPConn
}

func (conn *Conn) DialOriginalDestination(dontAssumeRemote bool) (*net.TCPConn, error) {
	remoteSocketAddress, err := tcpAddrToSocketAddr(conn.LocalAddr().(*net.TCPAddr))
	if err != nil {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("build destination socket address: %s", err)}
	}

	localSocketAddress, err := tcpAddrToSocketAddr(conn.RemoteAddr().(*net.TCPAddr))
	if err != nil {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("build local socket address: %s", err)}
	}

	log.Println("connect from", conn.RemoteAddr(), "to", conn.LocalAddr())

	fileDescriptor, err := syscall.Socket(tcpAddrFamily("tcp", conn.LocalAddr().(*net.TCPAddr), conn.RemoteAddr().(*net.TCPAddr)), syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket open: %s", err)}
	}

	// Set SO_MARK to apply the desired mark to the socket
	if err = syscall.SetsockoptInt(fileDescriptor, syscall.SOL_SOCKET, syscall.SO_MARK, 123); err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_MARK: %s", err)}
	}

	if err = syscall.SetsockoptInt(fileDescriptor, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_REUSEADDR: %s", err)}
	}

	if err = syscall.SetsockoptInt(fileDescriptor, syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: IP_TRANSPARENT: %s", err)}
	}

	if err = syscall.SetNonblock(fileDescriptor, noneBlock); err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_NONBLOCK: %s", err)}
	}
	localSocketAddress.Port = 0
	if !dontAssumeRemote {
		if err = syscall.Bind(fileDescriptor, localSocketAddress); err != nil {
			syscall.Close(fileDescriptor)
			return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket bind: %s", err)}
		}
	}

	if err = syscall.Connect(fileDescriptor, remoteSocketAddress); err != nil && !strings.Contains(err.Error(), "operation now in progress") {
		syscall.Close(fileDescriptor)
		log.Println("socket connect err:", err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket connect: %s", err)}
	}

	fdFile := os.NewFile(uintptr(fileDescriptor), fmt.Sprintf("net-tcp-dial-%s", conn.LocalAddr().String()))
	defer fdFile.Close()

	remoteConn, err := net.FileConn(fdFile)
	if err != nil {
		syscall.Close(fileDescriptor)
		log.Println("convert fd to connection err:", err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("convert file descriptor to connection: %s", err)}
	}
	log.Println("和server建立连接", "fdFile", fdFile.Name(), "fd", fileDescriptor, "server", conn.LocalAddr())
	return remoteConn.(*net.TCPConn), nil
}

func tcpAddrToSocketAddr(addr *net.TCPAddr) (*syscall.SockaddrInet4, error) {
	switch {
	case addr.IP.To4() != nil:
		ip := [4]byte{}
		copy(ip[:], addr.IP.To4())
		return &syscall.SockaddrInet4{Addr: ip, Port: addr.Port}, nil
	}
	return nil, fmt.Errorf("unsupported IP address type: %T", addr.IP)
}

func tcpAddrFamily(net string, laddr, raddr *net.TCPAddr) int {
	switch net[len(net)-1] {
	case '4':
		return syscall.AF_INET
	case '6':
		return syscall.AF_INET6
	}
	if (laddr == nil || laddr.IP.To4() != nil) &&
		(raddr == nil || laddr.IP.To4() != nil) {
		return syscall.AF_INET
	}
	return syscall.AF_INET6
}
