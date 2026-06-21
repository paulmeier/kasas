// A scheduled producer (ADR 0005): OnFetch receives the engine's {since, cursor}
// request and returns an ImportBatch the engine persists. The `source` field is a
// human label only; the host stamps plugin:<name>.
function OnFetch(req) {
  return {
    source: "acme-card",
    accounts: [
      {
        external_id: "acct-1",
        org: { id: "acme", name: "ACME" },
        name: "ACME Card",
        currency: "USD",
        transactions: [
          {
            external_id: "tx-1",
            amount: "-12.50",
            date: 1700000000,
            description: "Blue Bottle",
            payee: "Blue Bottle Coffee",
          },
        ],
      },
    ],
  };
}
