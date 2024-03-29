package gomux

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"time"

	pool "github.com/millken/gobuffers"
)

// ErrReset is returned when reading or writing on a reset stream.
var ErrReset = errors.New("stream reset")

// streamID is a convenience type for operating on stream IDs
type streamID struct {
	id        uint64
	initiator bool
}

// header computes the header for the given tag
func (id *streamID) header(tag uint64) uint64 {
	header := id.id<<3 | tag
	if !id.initiator {
		header--
	}
	return header
}

type Stream struct {
	id     streamID
	name   string
	dataIn chan []byte
	mp     *Multiplex

	extra []byte

	// exbuf is for holding the reference to the beginning of the extra slice
	// for later memory pool freeing
	exbuf []byte

	rDeadline, wDeadline pipeDeadline

	clLock       sync.Mutex
	closedRemote bool

	// Closed when the connection is reset.
	reset chan struct{}

	// Closed when the writer is closed (reset will also be closed)
	closedLocal  context.Context
	doCloseLocal context.CancelFunc
}

func (s *Stream) Name() string {
	return s.name
}

// tries to preload pending data
func (s *Stream) preloadData() {
	select {
	case read, ok := <-s.dataIn:
		if !ok {
			return
		}
		s.extra = read
		s.exbuf = read
	default:
	}
}

func (s *Stream) waitForData() error {
	select {
	case <-s.reset:
		// This is the only place where it's safe to return these.
		s.returnBuffers()
		return ErrReset
	case read, ok := <-s.dataIn:
		if !ok {
			return io.EOF
		}
		s.extra = read
		s.exbuf = read
		return nil
	case <-s.rDeadline.wait():
		return errTimeout
	}
}

func (s *Stream) returnBuffers() {
	if s.exbuf != nil {
		pool.Put(s.exbuf)
		s.exbuf = nil
		s.extra = nil
	}
	for {
		select {
		case read, ok := <-s.dataIn:
			if !ok {
				return
			}
			if read == nil {
				continue
			}
			pool.Put(read)
		default:
			return
		}
	}
}

func (s *Stream) Read(b []byte) (int, error) {
	select {
	case <-s.reset:
		return 0, ErrReset
	default:
	}
	if s.extra == nil {
		err := s.waitForData()
		if err != nil {
			return 0, err
		}
	}
	n := 0
	for s.extra != nil && n < len(b) {
		read := copy(b[n:], s.extra)
		n += read
		if read < len(s.extra) {
			s.extra = s.extra[read:]
		} else {
			if s.exbuf != nil {
				pool.Put(s.exbuf)
			}
			s.extra = nil
			s.exbuf = nil
			s.preloadData()
		}
	}
	return n, nil
}

func (s *Stream) Write(b []byte) (int, error) {
	var written int
	for written < len(b) {
		wl := len(b) - written
		if wl > MaxMessageSize {
			wl = MaxMessageSize
		}

		n, err := s.write(b[written : written+wl])
		if err != nil {
			return written, err
		}

		written += n
	}

	return written, nil
}

func (s *Stream) write(b []byte) (int, error) {
	if s.isClosed() {
		return 0, errors.New("cannot write to closed stream")
	}

	err := s.mp.sendMsg(s.wDeadline.wait(), s.id.header(messageTag), b)

	if err != nil {
		if err == context.Canceled {
			err = errors.New("cannot write to closed stream")
		}
		return 0, err
	}

	return len(b), nil
}

func (s *Stream) isClosed() bool {
	return s.closedLocal.Err() != nil
}

func (s *Stream) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), ResetStreamTimeout)
	defer cancel()

	err := s.mp.sendMsg(ctx.Done(), s.id.header(closeTag), nil)

	if s.isClosed() {
		return nil
	}

	s.clLock.Lock()
	remote := s.closedRemote
	s.clLock.Unlock()

	s.doCloseLocal()

	if remote {
		s.cancelDeadlines()
		s.mp.chLock.Lock()
		delete(s.mp.channels, s.id)
		s.mp.chLock.Unlock()
	}

	if err != nil && !s.mp.isShutdown() {
		log.Printf("Error closing stream: %s; killing connection", err.Error())
		s.mp.Close()
	}

	return err
}

func (s *Stream) Reset() error {
	s.clLock.Lock()

	// Don't reset when fully closed.
	if s.closedRemote && s.isClosed() {
		s.clLock.Unlock()
		return nil
	}

	// Don't reset twice.
	select {
	case <-s.reset:
		s.clLock.Unlock()
		return nil
	default:
	}

	close(s.reset)
	s.doCloseLocal()
	s.closedRemote = true
	s.cancelDeadlines()

	go s.mp.sendResetMsg(s.id.header(resetTag), true)

	s.clLock.Unlock()

	s.mp.chLock.Lock()
	delete(s.mp.channels, s.id)
	s.mp.chLock.Unlock()

	return nil
}

func (s *Stream) cancelDeadlines() {
	s.rDeadline.set(time.Time{})
	s.wDeadline.set(time.Time{})
}

func (s *Stream) SetDeadline(t time.Time) error {
	s.clLock.Lock()
	defer s.clLock.Unlock()

	if s.closedRemote && s.isClosed() {
		return errStreamClosed
	}

	if !s.closedRemote {
		s.rDeadline.set(t)
	}

	if !s.isClosed() {
		s.wDeadline.set(t)
	}

	return nil
}

func (s *Stream) SetReadDeadline(t time.Time) error {
	s.clLock.Lock()
	defer s.clLock.Unlock()

	if s.closedRemote {
		return errStreamClosed
	}

	s.rDeadline.set(t)
	return nil
}

func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.clLock.Lock()
	defer s.clLock.Unlock()

	if s.isClosed() {
		return errStreamClosed
	}

	s.wDeadline.set(t)
	return nil
}
