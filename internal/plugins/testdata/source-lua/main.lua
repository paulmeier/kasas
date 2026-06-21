-- A scheduled producer (ADR 0005): the engine calls OnFetch on the sync schedule
-- with req.since / req.cursor, and we return an ImportBatch the engine persists.
-- The `source` field here is a human label only; the host stamps plugin:<name>.
function OnFetch(req)
  return {
    source = "acme-card",
    accounts = {
      {
        external_id = "acct-1",
        org = { id = "acme", name = "ACME" },
        name = "ACME Card",
        currency = "USD",
        transactions = {
          {
            external_id = "tx-1",
            amount = "-12.50",
            date = 1700000000,
            description = "Blue Bottle",
            payee = "Blue Bottle Coffee",
          },
        },
      },
    },
  }
end
