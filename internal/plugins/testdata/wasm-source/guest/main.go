// A Go/WASM source plugin (ADR 0005): OnFetch returns a static batch the host
// persists. Test fixture for the source:provide path through the WASM runtime +
// the guest SDK's OnFetch support.
package main

import kasas "github.com/paulmeier/kasas/pluginsdk/kasas"

func init() {
	kasas.OnFetch(func(req *kasas.SourceRequest) (*kasas.Batch, error) {
		return &kasas.Batch{
			Source: "acme-card", // a human label; the host stamps plugin:<name>
			Accounts: []kasas.ImportAccount{{
				ExternalID: "acct-1",
				Org:        kasas.ImportOrg{ID: "acme", Name: "ACME"},
				Name:       "ACME Card",
				Currency:   "USD",
				Transactions: []kasas.ImportTransaction{{
					ExternalID:  "tx-1",
					Amount:      "-12.50",
					Date:        1700000000,
					Description: "Blue Bottle",
					Payee:       "Blue Bottle Coffee",
				}},
			}},
		}, nil
	})
}

func main() {} // required by -buildmode=c-shared, never runs
