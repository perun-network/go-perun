// Copyright 2026 - See NOTICE file for copyright holders.
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

package libp2p

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/pkg/errors"

	"perun.network/go-perun/channel"
	"perun.network/go-perun/channel/multi"
	"perun.network/go-perun/wallet"
)

const (
	requestCoordinationProtocol = "/ttp-coordinator/request/1.0.0"
	requestWitnessProtocol      = "/ttp-coordinator/witness/1.0.0"
	getStatusProtocol           = "/ttp-coordinator/status/1.0.0"

	responseStatusOK         = "ok"
	responseStatusError      = "error"
	responseStatusDuplicate  = "duplicate"
	responseStatusAccepted   = "accepted"
	responseStatusValidation = "validation_error"
	responseStatusPending    = "pending"
	responseStatusDecided    = "decided"
	responseStatusSubmitted  = "submitted"
)

// Response is the generic protocol response envelope returned by request and
// witness endpoints.
type Response struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// RequestCoordinationBody is the request payload for the coordination endpoint.
type RequestCoordinationBody struct {
	ChannelID string          `json:"channel_id"`
	Params    json.RawMessage `json:"params"`
	RequestID string          `json:"request_id"`
}

// RequestWitnessBody is the request payload for witness ingestion.
type RequestWitnessBody struct {
	ChannelID string          `json:"channel_id"`
	LedgerID  int             `json:"ledger_id"`
	State     json.RawMessage `json:"state"`
	Sigs      []string        `json:"sigs"`
	Source    string          `json:"source"`
}

// GetStatusQuery is the request payload for status queries.
type GetStatusQuery struct {
	ChannelID string `json:"channel_id"`
}

// StatusResponse is the response payload for status queries.
type StatusResponse struct {
	Status   string          `json:"status"`
	Decision json.RawMessage `json:"decision,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

// CoordinationRequestHandler handles inbound coordinator requests.
type CoordinationRequestHandler func(context.Context, channel.ID) error

// RequestCoordinationHandler handles the request coordination endpoint.
type RequestCoordinationHandler func(context.Context, RequestCoordinationBody) Response

// RequestWitnessHandler handles the witness endpoint.
type RequestWitnessHandler func(context.Context, RequestWitnessBody) Response

// GetStatusHandler handles the status endpoint.
type GetStatusHandler func(context.Context, GetStatusQuery) StatusResponse

// RelayCoordinationRequester sends coordination requests over the relay-backed
// libp2p transport.
type RelayCoordinationRequester struct {
	account *Account
}

var _ multi.CoordinationRequester = (*RelayCoordinationRequester)(nil)

// NewRelayCoordinationRequester creates a relay-backed requester.
func NewRelayCoordinationRequester(account *Account) *RelayCoordinationRequester {
	return &RelayCoordinationRequester{account: account}
}

// RequestCoordination asks the configured coordinator to coordinate settlement
// for the channel.
func (r *RelayCoordinationRequester) RequestCoordination(
	ctx context.Context,
	chID channel.ID,
	coordinator map[wallet.BackendID]wallet.Address,
) error {
	requestID, err := newRequestID()
	if err != nil {
		return errors.WithMessage(err, "creating request id")
	}

	resp, err := r.SubmitCoordinationRequest(ctx, coordinator, RequestCoordinationBody{
		ChannelID: encodeCoordinationChannelID(chID),
		Params:    json.RawMessage("{}"),
		RequestID: requestID,
	})
	if err != nil {
		return err
	}

	status := normalizeStatus(resp.Status)
	if status == responseStatusOK || status == responseStatusDuplicate || status == responseStatusAccepted {
		return nil
	}

	if resp.Reason != "" {
		return errors.New(resp.Reason)
	}

	if status == "" {
		return errors.New("empty status in coordination response")
	}

	return errors.Errorf("coordination request failed with status %q", resp.Status)
}

// SubmitCoordinationRequest sends a request payload to the coordinator and
// returns the raw response.
func (r *RelayCoordinationRequester) SubmitCoordinationRequest(
	ctx context.Context,
	coordinator map[wallet.BackendID]wallet.Address,
	body RequestCoordinationBody,
) (Response, error) {
	var resp Response
	err := r.callCoordinatorEndpoint(ctx, coordinator, requestCoordinationProtocol, body, &resp)
	if err != nil {
		return Response{}, err
	}

	return resp, nil
}

// RequestWitness sends a witness payload to the coordinator.
func (r *RelayCoordinationRequester) RequestWitness(
	ctx context.Context,
	coordinator map[wallet.BackendID]wallet.Address,
	body RequestWitnessBody,
) (Response, error) {
	var resp Response
	err := r.callCoordinatorEndpoint(ctx, coordinator, requestWitnessProtocol, body, &resp)
	if err != nil {
		return Response{}, err
	}

	return resp, nil
}

// GetStatus queries the coordinator status endpoint.
func (r *RelayCoordinationRequester) GetStatus(
	ctx context.Context,
	coordinator map[wallet.BackendID]wallet.Address,
	chID channel.ID,
) (StatusResponse, error) {
	query := GetStatusQuery{ChannelID: encodeCoordinationChannelID(chID)}

	var resp StatusResponse
	err := r.callCoordinatorEndpoint(ctx, coordinator, getStatusProtocol, query, &resp)
	if err != nil {
		return StatusResponse{}, err
	}

	return resp, nil
}

func (r *RelayCoordinationRequester) callCoordinatorEndpoint(
	ctx context.Context,
	coordinator map[wallet.BackendID]wallet.Address,
	protocolID coreprotocol.ID,
	req any,
	resp any,
) error {
	onChainAddr, err := pickCoordinatorAddress(coordinator)
	if err != nil {
		return errors.WithMessage(err, "selecting coordinator address")
	}

	coordinatorWireAddr, err := r.account.QueryOnChainAddress(onChainAddr)
	if err != nil {
		return errors.WithMessage(err, "querying coordinator relay address")
	}

	s, err := r.account.newRelayStream(ctx, coordinatorWireAddr.ID, protocolID)
	if err != nil {
		return errors.WithMessage(err, "opening coordinator stream")
	}
	defer s.Close()

	err = json.NewEncoder(s).Encode(req)
	if err != nil {
		return errors.WithMessage(err, "encoding coordinator request")
	}

	err = s.CloseWrite()
	if err != nil {
		return errors.WithMessage(err, "half-closing coordinator request stream")
	}

	err = json.NewDecoder(s).Decode(resp)
	if err != nil {
		return errors.WithMessage(err, "decoding coordinator response")
	}

	return nil
}

// SetCoordinationRequestHandler installs or replaces the coordinator request
// stream handler. Passing nil removes the handler.
func (acc *Account) SetCoordinationRequestHandler(handler CoordinationRequestHandler) {
	if handler == nil {
		acc.RemoveCoordinationRequestHandler()
		return
	}

	acc.SetRequestCoordinationHandler(func(ctx context.Context, req RequestCoordinationBody) Response {
		chID, err := decodeCoordinationChannelID(req.ChannelID)
		if err != nil {
			return Response{Status: responseStatusError, Reason: err.Error()}
		}

		err = handler(ctx, chID)
		if err != nil {
			return Response{Status: responseStatusError, Reason: err.Error()}
		}

		return Response{Status: responseStatusOK}
	})
}

// SetRequestCoordinationHandler installs or replaces the raw request endpoint
// handler. Passing nil removes the handler.
func (acc *Account) SetRequestCoordinationHandler(handler RequestCoordinationHandler) {
	if handler == nil {
		acc.RemoveCoordinationRequestHandler()
		return
	}

	acc.SetStreamHandler(requestCoordinationProtocol, func(s network.Stream) {
		defer s.Close()

		var req RequestCoordinationBody
		err := json.NewDecoder(s).Decode(&req)
		if err != nil {
			_ = writeResponse(s, Response{Status: responseStatusError, Reason: errors.WithMessage(err, "decoding coordination request").Error()})
			return
		}

		_ = writeResponse(s, handler(context.Background(), req))
	})
}

// RemoveCoordinationRequestHandler removes the coordinator request stream handler.
func (acc *Account) RemoveCoordinationRequestHandler() {
	acc.RemoveRequestCoordinationHandler()
}

// RemoveRequestCoordinationHandler removes the raw request endpoint handler.
func (acc *Account) RemoveRequestCoordinationHandler() {
	acc.RemoveStreamHandler(requestCoordinationProtocol)
}

// SetRequestWitnessHandler installs or replaces the witness endpoint handler.
func (acc *Account) SetRequestWitnessHandler(handler RequestWitnessHandler) {
	if handler == nil {
		acc.RemoveRequestWitnessHandler()
		return
	}

	acc.SetStreamHandler(requestWitnessProtocol, func(s network.Stream) {
		defer s.Close()

		var req RequestWitnessBody
		err := json.NewDecoder(s).Decode(&req)
		if err != nil {
			_ = writeResponse(s, Response{Status: responseStatusError, Reason: errors.WithMessage(err, "decoding witness request").Error()})
			return
		}

		_ = writeResponse(s, handler(context.Background(), req))
	})
}

// RemoveRequestWitnessHandler removes the witness endpoint handler.
func (acc *Account) RemoveRequestWitnessHandler() {
	acc.RemoveStreamHandler(requestWitnessProtocol)
}

// SetGetStatusHandler installs or replaces the status endpoint handler.
func (acc *Account) SetGetStatusHandler(handler GetStatusHandler) {
	if handler == nil {
		acc.RemoveGetStatusHandler()
		return
	}

	acc.SetStreamHandler(getStatusProtocol, func(s network.Stream) {
		defer s.Close()

		var req GetStatusQuery
		err := json.NewDecoder(s).Decode(&req)
		if err != nil {
			_ = writeStatusResponse(s, StatusResponse{Status: responseStatusError, Reason: errors.WithMessage(err, "decoding status query").Error()})
			return
		}

		_ = writeStatusResponse(s, handler(context.Background(), req))
	})
}

// RemoveGetStatusHandler removes the status endpoint handler.
func (acc *Account) RemoveGetStatusHandler() {
	acc.RemoveStreamHandler(getStatusProtocol)
}

func writeResponse(s network.Stream, resp Response) error {
	err := json.NewEncoder(s).Encode(resp)
	if err != nil {
		return errors.WithMessage(err, "encoding response")
	}

	return errors.WithMessage(s.CloseWrite(), "half-closing response stream")
}

func writeStatusResponse(s network.Stream, resp StatusResponse) error {
	err := json.NewEncoder(s).Encode(resp)
	if err != nil {
		return errors.WithMessage(err, "encoding status response")
	}

	return errors.WithMessage(s.CloseWrite(), "half-closing status response stream")
}

func pickCoordinatorAddress(coordinator map[wallet.BackendID]wallet.Address) (wallet.Address, error) {
	if len(coordinator) == 0 {
		return nil, errors.New("missing coordinator address")
	}

	backendIDs := make([]wallet.BackendID, 0, len(coordinator))
	for backendID := range coordinator {
		backendIDs = append(backendIDs, backendID)
	}
	sort.Slice(backendIDs, func(i, j int) bool {
		return backendIDs[i] < backendIDs[j]
	})

	for _, backendID := range backendIDs {
		addr := coordinator[backendID]
		if addr != nil {
			return addr, nil
		}
	}

	return nil, errors.New("missing non-nil coordinator address")
}

func (acc *Account) newRelayStream(ctx context.Context, remoteID peer.ID, protocolID coreprotocol.ID) (network.Stream, error) {
	fullAddr := acc.relayAddr + "/p2p/" + relayID + "/p2p-circuit/p2p/" + remoteID.String()
	peerMultiAddr, err := ma.NewMultiaddr(fullAddr)
	if err != nil {
		return nil, errors.WithMessage(err, "parsing relay circuit multiaddress")
	}

	peerAddrInfo, err := peer.AddrInfoFromP2pAddr(peerMultiAddr)
	if err != nil {
		return nil, errors.WithMessage(err, "converting relay circuit multiaddress")
	}

	err = acc.Connect(ctx, *peerAddrInfo)
	if err != nil {
		return nil, errors.WithMessage(err, "connecting to remote coordinator")
	}

	limitedConnTag := string(protocolID)
	if len(limitedConnTag) > 0 && limitedConnTag[0] == '/' {
		limitedConnTag = limitedConnTag[1:]
	}

	s, err := acc.NewStream(network.WithAllowLimitedConn(ctx, limitedConnTag), peerAddrInfo.ID, protocolID)
	if err != nil {
		return nil, errors.WithMessage(err, "creating coordination stream")
	}

	return s, nil
}

func encodeCoordinationChannelID(chID channel.ID) string {
	return hex.EncodeToString(chID[:])
}

func decodeCoordinationChannelID(raw string) (channel.ID, error) {
	var chID channel.ID
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return chID, errors.WithMessage(err, "decoding channel id")
	}

	if len(decoded) != len(chID) {
		return chID, errors.Errorf("invalid channel id length: got %d, want %d", len(decoded), len(chID))
	}

	copy(chID[:], decoded)
	return chID, nil
}

func normalizeStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func newRequestID() (string, error) {
	b := make([]byte, 16)
	_, err := cryptorand.Read(b)
	if err != nil {
		return "", err
	}

	// Set UUID version (4) and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	hexID := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexID[0:8], hexID[8:12], hexID[12:16], hexID[16:20], hexID[20:32]), nil
}
