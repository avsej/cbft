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

package cbft

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/couchbase/gomemcached/client"
	log "github.com/couchbaselabs/clog"
	"github.com/couchbaselabs/go-couchbase"
)

func init() {
	RegisterFeedType("couchbase-tap",
		&FeedType{
			Start:       StartTAPFeed,
			Partitions:  CouchbasePartitions,
			Public:      false,
			Description: "couchbase-tap - Couchbase Server/Cluster data source, via TAP protocol",
			StartSample: &TAPFeedParams{},
		})
}

func StartTAPFeed(mgr *Manager, feedName, indexName, indexUUID,
	sourceType, bucketName, bucketUUID, params string, dests map[string]Dest) error {
	feed, err := NewTAPFeed(feedName, mgr.server, "default",
		bucketName, bucketUUID, params, BasicPartitionFunc, dests)
	if err != nil {
		return fmt.Errorf("error: could not prepare TAP stream to server: %s,"+
			" bucketName: %s, indexName: %s, err: %v",
			mgr.server, bucketName, indexName, err)
	}
	err = feed.Start()
	if err != nil {
		return fmt.Errorf("error: could not start tap feed, server: %s, err: %v",
			mgr.server, err)
	}
	err = mgr.registerFeed(feed)
	if err != nil {
		feed.Close()
		return err
	}
	return nil
}

// A TAPFeed uses TAP protocol to dump data from a couchbase data source.
type TAPFeed struct {
	name       string
	url        string
	poolName   string
	bucketName string
	bucketUUID string
	params     *TAPFeedParams
	pf         DestPartitionFunc
	dests      map[string]Dest
	closeCh    chan bool
	doneCh     chan bool
	doneErr    error
	doneMsg    string
}

type TAPFeedParams struct {
	BackoffFactor float32 `json:"backoffFactor"`
	SleepInitMS   int     `json:"sleepInitMS"`
	SleepMaxMS    int     `json:"sleepMaxMS"`
}

func NewTAPFeed(name, url, poolName, bucketName, bucketUUID, paramsStr string,
	pf DestPartitionFunc, dests map[string]Dest) (*TAPFeed, error) {
	params := &TAPFeedParams{}
	if paramsStr != "" {
		err := json.Unmarshal([]byte(paramsStr), params)
		if err != nil {
			return nil, err
		}
	}

	return &TAPFeed{
		name:       name,
		url:        url,
		poolName:   poolName,
		bucketName: bucketName,
		bucketUUID: bucketUUID,
		params:     params,
		pf:         pf,
		dests:      dests,
		closeCh:    make(chan bool),
		doneCh:     make(chan bool),
		doneErr:    nil,
		doneMsg:    "",
	}, nil
}

func (t *TAPFeed) Name() string {
	return t.name
}

func (t *TAPFeed) Start() error {
	log.Printf("TAPFeed.Start, name: %s", t.Name())

	backoffFactor := t.params.BackoffFactor
	if backoffFactor <= 0.0 {
		backoffFactor = FEED_BACKOFF_FACTOR
	}
	sleepInitMS := t.params.SleepInitMS
	if sleepInitMS <= 0 {
		sleepInitMS = FEED_SLEEP_INIT_MS
	}
	sleepMaxMS := t.params.SleepMaxMS
	if sleepMaxMS <= 0 {
		sleepMaxMS = FEED_SLEEP_MAX_MS
	}

	go ExponentialBackoffLoop(t.Name(),
		func() int {
			progress, err := t.feed()
			if err != nil {
				log.Printf("TAPFeed name: %s, progress: %d, err: %v",
					t.Name(), progress, err)
			}
			return progress
		},
		sleepInitMS, backoffFactor, sleepMaxMS)

	return nil
}

func (t *TAPFeed) feed() (int, error) {
	select {
	case <-t.closeCh:
		t.doneErr = nil
		t.doneMsg = "closeCh closed"
		close(t.doneCh)
		return -1, nil
	default:
	}

	bucket, err := couchbase.GetBucket(t.url, t.poolName, t.bucketName)
	if err != nil {
		return 0, err
	}
	defer bucket.Close()

	if t.bucketUUID != "" && t.bucketUUID != bucket.UUID {
		bucket.Close()
		return -1, fmt.Errorf("error: mismatched bucket uuid,"+
			"bucketName: %s, bucketUUID: %s, bucket.UUID: %s",
			t.bucketName, t.bucketUUID, bucket.UUID)
	}

	args := memcached.TapArguments{}

	vbuckets, err := ParsePartitionsToVBucketIds(t.dests)
	if err != nil {
		return -1, err
	}
	if len(vbuckets) > 0 {
		args.VBuckets = vbuckets
	}

	feed, err := bucket.StartTapFeed(&args)
	if err != nil {
		return 0, err
	}
	defer feed.Close()

	// TODO: maybe TAPFeed should do a rollback to zero if it finds it
	// needs to do a full backfill.
	// TODO: this TAPFeed implementation currently only works against
	// a couchbase cluster that has just a single node.

	log.Printf("TapFeed: running, url: %s,"+
		" poolName: %s, bucketName: %s, vbuckets: %#v",
		t.url, t.poolName, t.bucketName, vbuckets)

loop:
	for {
		select {
		case <-t.closeCh:
			t.doneErr = nil
			t.doneMsg = "closeCh closed"
			close(t.doneCh)
			return -1, nil

		case req, alive := <-feed.C:
			if !alive {
				break loop
			}

			log.Printf("TapFeed: received from url: %s,"+
				" poolName: %s, bucketName: %s, opcode: %s, req: %#v",
				t.url, t.poolName, t.bucketName, req.Opcode, req)

			partition, dest, err :=
				VBucketIdToPartitionDest(t.pf, t.dests, req.VBucket, req.Key)
			if err != nil {
				return 1, err
			}

			if req.Opcode == memcached.TapMutation {
				err = dest.OnDataUpdate(partition, req.Key, 0, req.Value)
			} else if req.Opcode == memcached.TapDeletion {
				err = dest.OnDataDelete(partition, req.Key, 0)
			}
			if err != nil {
				return 1, err
			}
		}
	}

	return 1, nil
}

func (t *TAPFeed) Close() error {
	select {
	case <-t.doneCh:
		return t.doneErr
	default:
	}

	close(t.closeCh)
	<-t.doneCh
	return t.doneErr
}

func (t *TAPFeed) Dests() map[string]Dest {
	return t.dests
}

func (t *TAPFeed) Stats(w io.Writer) error {
	_, err := w.Write([]byte("{}"))
	return err
}

// ----------------------------------------------------------------

func ParsePartitionsToVBucketIds(dests map[string]Dest) ([]uint16, error) {
	vbuckets := make([]uint16, 0, len(dests))
	for partition, _ := range dests {
		if partition != "" {
			vbId, err := strconv.Atoi(partition)
			if err != nil {
				return nil, fmt.Errorf("error: could not parse partition: %s, err: %v",
					partition, err)
			}
			vbuckets = append(vbuckets, uint16(vbId))
		}
	}
	return vbuckets, nil
}

func VBucketIdToPartitionDest(pf DestPartitionFunc,
	dests map[string]Dest, vbucketId uint16, key []byte) (
	partition string, dest Dest, err error) {
	if vbucketId < uint16(len(vbucketIdStrings)) {
		partition = vbucketIdStrings[vbucketId]
	}
	if partition == "" {
		partition = fmt.Sprintf("%d", vbucketId)
	}
	dest, err = pf(partition, key, dests)
	if err != nil {
		return "", nil, fmt.Errorf("error: VBucketIdToPartitionDest,"+
			" partition func, vbucketId: %d, err: %v", vbucketId, err)
	}
	return partition, dest, err
}

var vbucketIdStrings []string

func init() {
	vbucketIdStrings = make([]string, 1024)
	for i := 0; i < len(vbucketIdStrings); i++ {
		vbucketIdStrings[i] = fmt.Sprintf("%d", i)
	}
}

// ----------------------------------------------------------------

func CouchbasePartitions(sourceType, sourceName, sourceUUID, sourceParams,
	server string) ([]string, error) {
	poolName := "default" // TODO: Parameterize poolName.
	bucketName := sourceName

	// TODO: how the halloween does GetBucket() api work without explicit auth?
	bucket, err := couchbase.GetBucket(server, poolName, bucketName)
	if err != nil {
		return nil, fmt.Errorf("error: DataSourcePartitions/couchbase"+
			" failed GetBucket, server: %s, poolName: %s, bucketName: %s, err: %v",
			server, poolName, bucketName, err)
	}
	defer bucket.Close()

	vbm := bucket.VBServerMap()
	if vbm == nil {
		return nil, fmt.Errorf("error: DataSourcePartitions/couchbase"+
			" no VBServerMap, server: %s, poolName: %s, bucketName: %s, err: %v",
			server, poolName, bucketName, err)
	}

	// NOTE: We assume that vbucket numbers are continuous
	// integers starting from 0.
	numVBuckets := len(vbm.VBucketMap)
	rv := make([]string, numVBuckets)
	for i := 0; i < numVBuckets; i++ {
		rv[i] = strconv.Itoa(i)
	}
	return rv, nil
}
