// Copyright (c) 2016, Google Inc.
//
// Permission to use, copy, modify, and/or distribute this software for any
// purpose with or without fee is hereby granted, provided that the above
// copyright notice and this permission notice appear in all copies.
//
// THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
// WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
// MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY
// SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
// WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION
// OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF OR IN
// CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

package ctls

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

type flowType int

const (
	readFlow flowType = iota
	writeFlow
	specialFlow
)

type flow struct {
	flowType flowType
	message  string
	data     []byte
}

// recordingConn is a net.Conn that records the traffic that passes through it.
// WriteTo can be used to produce output that can be later be loaded with
// ParseTestData.
type recordingConn struct {
	net.Conn
	sync.Mutex
	flows       []flow
	isDatagram  bool
	local, peer string
}

func (r *recordingConn) appendFlow(flowType flowType, message string, data []byte) {
	r.Lock()
	defer r.Unlock()

	if l := len(r.flows); flowType == specialFlow || r.isDatagram || l == 0 || r.flows[l-1].flowType != flowType {
		buf := make([]byte, len(data))
		copy(buf, data)
		r.flows = append(r.flows, flow{flowType, message, buf})
	} else {
		r.flows[l-1].data = append(r.flows[l-1].data, data...)
	}
}

func (r *recordingConn) Read(b []byte) (n int, err error) {
	if n, err = r.Conn.Read(b); n == 0 {
		return
	}
	r.appendFlow(readFlow, "", b[:n])
	return
}

func (r *recordingConn) Write(b []byte) (n int, err error) {
	if n, err = r.Conn.Write(b); n == 0 {
		return
	}
	r.appendFlow(writeFlow, "", b[:n])
	return
}

// LogSpecial appends an entry to the record of type 'special'.
func (r *recordingConn) LogSpecial(message string, data []byte) {
	r.appendFlow(specialFlow, message, data)
}

// WriteTo writes hex dumps to w that contains the recorded traffic.
func (r *recordingConn) WriteTo(w io.Writer) {
	fmt.Fprintf(w, ">>> ctls is %s, shim is %s\n", r.local, r.peer)
	for i, flow := range r.flows {
		switch flow.flowType {
		case readFlow:
			fmt.Fprintf(w, ">>> Flow %d (%s to %s)\n", i+1, r.peer, r.local)
		case writeFlow:
			fmt.Fprintf(w, ">>> Flow %d (%s to %s)\n", i+1, r.local, r.peer)
		case specialFlow:
			fmt.Fprintf(w, ">>> Flow %d %q\n", i+1, flow.message)
		}

		if flow.data != nil {
			dumper := hex.Dumper(w)
			dumper.Write(flow.data)
			dumper.Close()
		}
	}
}

func (r *recordingConn) Transcript() []byte {
	var ret []byte
	for _, flow := range r.flows {
		if flow.flowType != writeFlow {
			continue
		}
		if r.isDatagram {
			// Prepend a length prefix to preserve packet boundaries.
			ret = append(ret, byte(len(flow.data)>>16), byte(len(flow.data)>>8), byte(len(flow.data)))
		}
		ret = append(ret, flow.data...)
	}
	return ret
}

func parseTestData(r io.Reader) (flows [][]byte, err error) {
	var currentFlow []byte

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		// If the line starts with ">>> " then it marks the beginning
		// of a new flow.
		if strings.HasPrefix(line, ">>> ") {
			if len(currentFlow) > 0 || len(flows) > 0 {
				flows = append(flows, currentFlow)
				currentFlow = nil
			}
			continue
		}

		// Otherwise the line is a line of hex dump that looks like:
		// 00000170  fc f5 06 bf (...)  |.....X{&?......!|
		// (Some bytes have been omitted from the middle section.)

		if i := strings.IndexByte(line, ' '); i >= 0 {
			line = line[i:]
		} else {
			return nil, errors.New("invalid test data")
		}

		if i := strings.IndexByte(line, '|'); i >= 0 {
			line = line[:i]
		} else {
			return nil, errors.New("invalid test data")
		}

		hexBytes := strings.Fields(line)
		for _, hexByte := range hexBytes {
			val, err := strconv.ParseUint(hexByte, 16, 8)
			if err != nil {
				return nil, errors.New("invalid hex byte in test data: " + err.Error())
			}
			currentFlow = append(currentFlow, byte(val))
		}
	}

	if len(currentFlow) > 0 {
		flows = append(flows, currentFlow)
	}

	return flows, nil
}
