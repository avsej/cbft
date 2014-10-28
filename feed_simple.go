//  Copyright (c) 2014 Couchbase, Inc.
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the
//  License. You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
//  Unless required by applicable law or agreed to in writing,
//  software distributed under the License is distributed on an "AS
//  IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
//  express or implied. See the License for the specific language
//  governing permissions and limitations under the License.

package main

import (
	"fmt"
	log "github.com/couchbaselabs/clog"
)

// A SimpleFeed is uses a local, in-memory channel as its Stream
// datasource.  It's useful, amongst other things, for testing.
type SimpleFeed struct {
	name    string
	streams map[string]Stream
	closeCh chan bool
	doneCh  chan bool
	doneErr error
	doneMsg string
	source  Stream
	pf      StreamPartitionFunc
}

func (t *SimpleFeed) Source() Stream {
	return t.source
}

func NewSimpleFeed(name string, source Stream, pf StreamPartitionFunc,
	streams map[string]Stream) (*SimpleFeed, error) {
	return &SimpleFeed{
		name:    name,
		streams: streams,
		closeCh: make(chan bool),
		doneCh:  make(chan bool),
		doneErr: nil,
		doneMsg: "",
		source:  source,
		pf:      pf,
	}, nil
}

func (t *SimpleFeed) Name() string {
	return t.name
}

func (t *SimpleFeed) Start() error {
	log.Printf("SimpleFeed.Start, name: %s", t.Name())
	go t.feed()
	return nil
}

func (t *SimpleFeed) feed() {
	for {
		select {
		case <-t.closeCh:
			t.doneErr = nil
			t.doneMsg = "closeCh closed"
			close(t.doneCh)
			return

		case req, alive := <-t.source:
			if !alive {
				t.waitForClose("source closed", nil)
				return
			}

			stream, err := t.pf(req, t.streams)
			if err != nil {
				t.waitForClose("partition func error",
					fmt.Errorf("error: SimpleFeed pf on req: %#v, err: %v",
						req, err))
				return
			}

			var doneChOrig chan error
			var doneCh chan error
			wantWaitForClose := ""

			switch req := req.(type) {
			case *StreamEnd:
				doneCh := make(chan error)
				req.DoneCh, doneChOrig = doneCh, req.DoneCh
				wantWaitForClose = "source stream end"

			case *StreamFlush:
				doneCh := make(chan error)
				req.DoneCh, doneChOrig = doneCh, req.DoneCh

			case *StreamRollback:
				doneCh := make(chan error)
				req.DoneCh, doneChOrig = doneCh, req.DoneCh
				wantWaitForClose = "source stream rollback"

			case *StreamSnapshot:
				doneCh := make(chan error)
				req.DoneCh, doneChOrig = doneCh, req.DoneCh

			case *StreamUpdate:
			case *StreamDelete:
			}

			stream <- req

			if doneCh != nil {
				err = <-doneCh
			}

			if doneChOrig != nil {
				if err != nil {
					doneChOrig <- err
				}
				close(doneChOrig)
			}

			if wantWaitForClose != "" {
				t.waitForClose(wantWaitForClose, nil)
				return
			}
		}
	}
}

func (t *SimpleFeed) waitForClose(msg string, err error) {
	<-t.closeCh
	t.doneErr = err
	t.doneMsg = msg
	close(t.doneCh)
}

func (t *SimpleFeed) Close() error {
	close(t.closeCh)
	<-t.doneCh
	return t.doneErr
}

func (t *SimpleFeed) Streams() map[string]Stream {
	return t.streams
}