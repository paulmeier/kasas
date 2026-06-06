-- A tiny budgeting plugin: when a transaction's description contains the
-- configured keyword, tag it and flag it via a schema extension.

local function classify(txn)
  if txn.description and string.find(string.lower(txn.description), kasas.config.keyword, 1, true) then
    kasas.apply_labels(txn.id, { category = "food" })
    kasas.set_extension(txn.id, "budgeting.flagged", true)
    kasas.log("info", "tagged transaction", { id = txn.id })
  end
end

function OnTransactionCreate(txn)
  classify(txn)
end

function OnTransactionUpdate(txn)
  classify(txn)
end
