package master

import (
	"context"
	"io"

	"github.com/flipkart-incubator/dkv/internal/server/storage"
	"github.com/flipkart-incubator/dkv/internal/server/sync/raftpb"
	"github.com/flipkart-incubator/dkv/pkg/serverpb"
	nexus_api "github.com/flipkart-incubator/nexus/pkg/api"
	"github.com/gogo/protobuf/proto"
)

// A DKVService represents a service for serving key value data
// along with exposing all mutations as a replication stream.
type DKVService interface {
	io.Closer
	serverpb.DKVServer
	serverpb.DKVReplicationServer
}

type standaloneService struct {
	store storage.KVStore
	cp    storage.ChangePropagator
}

// NewStandaloneService creates a standalone variant of the DKVService
// that works only with the local storage.
func NewStandaloneService(store storage.KVStore, cp storage.ChangePropagator) *standaloneService {
	return &standaloneService{store, cp}
}

func (ss *standaloneService) Put(ctx context.Context, putReq *serverpb.PutRequest) (*serverpb.PutResponse, error) {
	if err := ss.store.Put(putReq.Key, putReq.Value); err != nil {
		return &serverpb.PutResponse{Status: newErrorStatus(err)}, err
	}
	return &serverpb.PutResponse{Status: newEmptyStatus()}, nil
}

func (ss *standaloneService) Get(ctx context.Context, getReq *serverpb.GetRequest) (*serverpb.GetResponse, error) {
	if readResults, err := ss.store.Get(getReq.Key); err != nil {
		return &serverpb.GetResponse{Status: newErrorStatus(err), Value: nil}, err
	} else {
		return &serverpb.GetResponse{Status: newEmptyStatus(), Value: readResults[0]}, nil
	}
}

func (ss *standaloneService) MultiGet(ctx context.Context, multiGetReq *serverpb.MultiGetRequest) (*serverpb.MultiGetResponse, error) {
	if readResults, err := ss.store.Get(multiGetReq.Keys...); err != nil {
		return &serverpb.MultiGetResponse{Status: newErrorStatus(err), Values: nil}, err
	} else {
		return &serverpb.MultiGetResponse{Status: newEmptyStatus(), Values: readResults}, nil
	}
}

func (ss *standaloneService) GetChanges(ctx context.Context, getChngsReq *serverpb.GetChangesRequest) (*serverpb.GetChangesResponse, error) {
	latestChngNum, _ := ss.cp.GetLatestCommittedChangeNumber()
	if getChngsReq.FromChangeNumber > latestChngNum {
		return &serverpb.GetChangesResponse{Status: newEmptyStatus(), MasterChangeNumber: latestChngNum, NumberOfChanges: 0}, nil
	}
	if chngs, err := ss.cp.LoadChanges(getChngsReq.FromChangeNumber, int(getChngsReq.MaxNumberOfChanges)); err != nil {
		return &serverpb.GetChangesResponse{Status: newErrorStatus(err)}, err
	} else {
		numChngs := uint32(len(chngs))
		return &serverpb.GetChangesResponse{Status: newEmptyStatus(), MasterChangeNumber: latestChngNum, NumberOfChanges: numChngs, Changes: chngs}, nil
	}
}

func (ss *standaloneService) Close() error {
	ss.store.Close()
	return nil
}

type distributedService struct {
	*standaloneService
	raftRepl nexus_api.RaftReplicator
}

// NewDistributedService creates a distributed variant of the DKV service
// that attempts to replicate data across multiple replicas over Nexus.
func NewDistributedService(kvs storage.KVStore, cp storage.ChangePropagator, raftRepl nexus_api.RaftReplicator) *distributedService {
	return &distributedService{NewStandaloneService(kvs, cp), raftRepl}
}

func (ds *distributedService) Put(ctx context.Context, putReq *serverpb.PutRequest) (*serverpb.PutResponse, error) {
	intReq := new(raftpb.InternalRaftRequest)
	intReq.Put = putReq
	if reqBts, err := proto.Marshal(intReq); err != nil {
		return &serverpb.PutResponse{Status: newErrorStatus(err)}, err
	} else {
		if _, err := ds.raftRepl.Replicate(ctx, reqBts); err != nil {
			return &serverpb.PutResponse{Status: newErrorStatus(err)}, err
		}
		return &serverpb.PutResponse{Status: newEmptyStatus()}, nil
	}
}

func (ds *distributedService) Get(ctx context.Context, getReq *serverpb.GetRequest) (*serverpb.GetResponse, error) {
	// TODO: Check for consistency level of GetRequest and process this either via local state or RAFT
	return ds.standaloneService.Get(ctx, getReq)
}

func (ds *distributedService) MultiGet(ctx context.Context, multiGetReq *serverpb.MultiGetRequest) (*serverpb.MultiGetResponse, error) {
	// TODO: Check for consistency level of MultiGetRequest and process this either via local state or RAFT
	return ds.standaloneService.MultiGet(ctx, multiGetReq)
}

func (ds *distributedService) Close() error {
	ds.raftRepl.Stop()
	return nil
}

func newErrorStatus(err error) *serverpb.Status {
	return &serverpb.Status{Code: -1, Message: err.Error()}
}

func newEmptyStatus() *serverpb.Status {
	return &serverpb.Status{Code: 0, Message: ""}
}
