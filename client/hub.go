// Copyright 2019 - See NOTICE file for copyright holders.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/pkg/errors"

	"perun.network/go-perun/channel"
	"perun.network/go-perun/log"
	"perun.network/go-perun/pkg/io"
	pkgsync "perun.network/go-perun/pkg/sync"
)

// Do not copy a Hub instanfc.
type Hub struct {
	pkgsync.Closer
	conns []net.Conn

	host, network string
}

func NewHub(ip string, port uint16) *Hub {
	if ip == "" {
		ip = "127.0.0.1"
	}
	hub := &Hub{
		host:    formatHost(ip, port),
		network: "tcp",
	}
	hub.Closer.OnClose(func() {
		for _, conn := range hub.conns {
			conn.Close()
		}
	})
	return hub
}

func (h *Hub) SetupPassive(numPartners int) error {
	if numPartners < 1 {
		return errors.New("invalid numPartners")
	}
	listener, err := net.Listen(h.network, h.host)
	if err != nil {
		return errors.WithMessage(err, "listening")
	}
	log.Debug("Listening on: ", h.host)
	// Accept `numPartners` incoming connection
	for i := 0; i < numPartners; i++ {
		conn, err := listener.Accept()
		if err != nil {
			return errors.WithMessage(err, "accepting connection")
		}
		log.Debugf("Accepted conn: %s, %d/%d", conn.RemoteAddr().String(), i+1, numPartners)
		h.conns = append(h.conns, conn)
	}
	return nil
}

func (h *Hub) SetupActive() error {
	log.Debug("Dialing: ", h.host)
	dialer := net.Dialer{Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, h.network, h.host)
	if err != nil {
		return errors.WithMessage(err, "dialing")
	}
	log.Debug("Connected to: ", h.host)
	h.conns = []net.Conn{conn}
	return nil
}

// does not block
func (h *Hub) recv() (<-chan *channel.State, <-chan error) {
	states := make(chan *channel.State, 10)
	errs := make(chan error, 10)
	for _, conn := range h.conns {
		conn := conn
		go func() {
			state := new(channel.State)
			if err := io.Decode(conn, state); err != nil {
				errs <- errors.WithMessage(err, "decoding or reading state")
				return
			}
			states <- state
		}()
	}
	return states, errs
}

func (h *Hub) send(state *channel.State) error {
	for _, conn := range h.conns {
		err := io.Encode(conn, state)
		return errors.WithMessage(err, "encoding or sending state")
	}
	return nil
}

func formatHost(ip string, port uint16) string {
	return fmt.Sprintf("%s:%d", ip, port)
}
