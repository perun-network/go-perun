package multibackend

import (
	"context"
	"fmt"

	"perun.network/go-perun/channel"
	"polycry.pt/poly-go/errors"
)

type assetKey string

// makeAssetKey creates an asset key from an asset.
func makeAssetKey(a channel.Asset) (assetKey, error) {
	b, err := a.MarshalBinary()
	return assetKey(b), err
}

// Funder is an implmentation of the Funder interface that supports multiple
// backends.
type Funder struct {
	funders map[assetKey]channel.Funder
}

func (f *Funder) RegisterAsset(a channel.Asset, af channel.Funder) error {
	k, err := makeAssetKey(a)
	if err != nil {
		return err
	}

	f.funders[k] = af
	return nil
}

// Fund deposit funds into a channel and waits until funding by
// other peers is complete.
func (f *Funder) Fund(ctx context.Context, req channel.FundingReq) error {
	g := errors.NewGatherer()

	funders, err := f.fundersForAssets(req.State.Assets)
	if err != nil {
		return err
	}

	for _, subf := range funders {
		// TODO: Need to adapt each funder implementations so that it
		// understands when an asset is not available on a network.
		g.Go(func() error { return subf.Fund(ctx, req) })
	}

	if !g.WaitDoneOrFailedCtx(ctx) {
		return ctx.Err()
	}
	return g.Err()
}

// fundersForAssets retrieves the funders that are associates with `assets`.
func (f *Funder) fundersForAssets(assets []channel.Asset) ([]channel.Funder, error) {
	// Gather the funders in a map to make sure that they are unique.
	funders := make(map[channel.Funder]struct{})
	for _, a := range assets {
		ak, err := makeAssetKey(a)
		if err != nil {
			return nil, err
		}

		af, ok := f.funders[ak]
		if !ok {
			return nil, fmt.Errorf("could not find funder for asset: %v", a)
		}

		funders[af] = struct{}{}
	}

	// Turn the map into a list so that we can return the funders.
	fundersList := make([]channel.Funder, len(funders))
	i := 0
	for af := range funders {
		fundersList[i] = af
		i++
	}

	return fundersList, nil
}
