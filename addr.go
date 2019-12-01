package gomux

import (
	"fmt"
	"net"
)

// hasAddr is used to get the address from the underlying connection
type hasAddr interface {
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// gomuxAddr is used when we cannot get the underlying address
type gomuxAddr struct {
	Addr string
}

func (*gomuxAddr) Network() string {
	return "gomux"
}

func (y *gomuxAddr) String() string {
	return fmt.Sprintf("gomux:%s", y.Addr)
}

// Addr is used to get the address of the listener.
func (s *Multiplex) Addr() net.Addr {
	return s.LocalAddr()
}

// LocalAddr is used to get the local address of the
// underlying connection.
func (s *Multiplex) LocalAddr() net.Addr {
	addr, ok := s.con.(hasAddr)
	if !ok {
		return &gomuxAddr{"local"}
	}
	return addr.LocalAddr()
}

// RemoteAddr is used to get the address of remote end
// of the underlying connection
func (s *Multiplex) RemoteAddr() net.Addr {
	addr, ok := s.con.(hasAddr)
	if !ok {
		return &gomuxAddr{"remote"}
	}
	return addr.RemoteAddr()
}

// LocalAddr returns the local address
func (s *Stream) LocalAddr() net.Addr {
	return s.mp.LocalAddr()
}

// RemoteAddr returns the remote address
func (s *Stream) RemoteAddr() net.Addr {
	return s.mp.RemoteAddr()
}
