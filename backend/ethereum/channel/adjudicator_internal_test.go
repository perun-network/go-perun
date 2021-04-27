// Copyright 2020 - See NOTICE file for copyright holders.
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

package channel

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"perun.network/go-perun/backend/ethereum/bindings/adjudicator"
	"perun.network/go-perun/channel"
	channeltest "perun.network/go-perun/channel/test"
	pkgtest "perun.network/go-perun/pkg/test"
)

func Test_toEthSubParamsAndStates(t *testing.T) {
	var (
		rng    = pkgtest.Prng(t)
		assert = assert.New(t)
	)

	tests := []struct {
		title string
		setup func() (channel *channel.Channel, subChannels channel.ChannelMap, expectedParams []adjudicator.ChannelParams, expectedStates []adjudicator.ChannelState)
	}{
		{
			title: "nil map gives nil slice",
			setup: func() (channel *channel.Channel, subChannels channel.ChannelMap, expectedParams []adjudicator.ChannelParams, expectedStates []adjudicator.ChannelState) {
				return channeltest.NewRandomChannel(rng), nil, nil, nil
			},
		},
		{
			title: "fresh map gives nil slice",
			setup: func() (channel *channel.Channel, subChannels channel.ChannelMap, expectedParams []adjudicator.ChannelParams, expectedStates []adjudicator.ChannelState) {
				return channeltest.NewRandomChannel(rng), nil, nil, nil
			},
		},
		{
			title: "1 layer of sub-channels",
			setup: func() (channel *channel.Channel, subChannels channel.ChannelMap, expectedParams []adjudicator.ChannelParams, expectedStates []adjudicator.ChannelState) {
				// ch[0]( ch[1], ch[2], ch[3] )
				ch := genChannels(rng, 4)
				ch[0].AddSubAlloc(*ch[1].ToSubAlloc())
				ch[0].AddSubAlloc(*ch[2].ToSubAlloc())
				ch[0].AddSubAlloc(*ch[3].ToSubAlloc())
				return ch[0], toChannelMap(ch[1:]...), toEthParams(ch[1:]...), toEthStates(ch[1:]...)
			},
		},
		{
			title: "2 layers of sub-channels",
			setup: func() (channel *channel.Channel, subChannels channel.ChannelMap, expectedParams []adjudicator.ChannelParams, expectedStates []adjudicator.ChannelState) {
				// ch[0]( ch[1]( ch[2], ch[3] ), ch[4], ch[5] (ch[6] ) )
				ch := genChannels(rng, 7)
				ch[0].AddSubAlloc(*ch[1].ToSubAlloc())
				ch[0].AddSubAlloc(*ch[4].ToSubAlloc())
				ch[0].AddSubAlloc(*ch[5].ToSubAlloc())
				ch[1].AddSubAlloc(*ch[2].ToSubAlloc())
				ch[1].AddSubAlloc(*ch[3].ToSubAlloc())
				ch[5].AddSubAlloc(*ch[6].ToSubAlloc())
				return ch[0], toChannelMap(ch[1:]...), toEthParams(ch[1:]...), toEthStates(ch[1:]...)
			},
		},
	}

	for _, tc := range tests {
		ch, subChannels, expectedParams, expectedStates := tc.setup()
		gotParams, gotStates := toEthSubParamsAndState(ch.State, subChannels)
		assert.Equal(expectedParams, gotParams, tc.title)
		assert.Equal(expectedStates, gotStates, tc.title)
	}
}

func genChannels(rng *rand.Rand, n int) (channels []*channel.Channel) {
	channels = make([]*channel.Channel, n)
	for i := range channels {
		channels[i] = channeltest.NewRandomChannel(rng)
	}
	return
}

func toChannelMap(channels ...*channel.Channel) (_channels channel.ChannelMap) {
	_channels = channel.MakeChannelMap()
	_channels.Add(channels...)
	return
}

func toEthParams(channels ...*channel.Channel) (params []adjudicator.ChannelParams) {
	params = make([]adjudicator.ChannelParams, len(channels))
	for i, s := range channels {
		params[i] = ToEthParams(s.Params)
	}
	return
}

func toEthStates(channels ...*channel.Channel) (states []adjudicator.ChannelState) {
	states = make([]adjudicator.ChannelState, len(channels))
	for i, s := range channels {
		states[i] = ToEthState(s.State)
	}
	return
}
