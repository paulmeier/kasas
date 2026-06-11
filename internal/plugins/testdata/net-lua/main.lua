function OnTransactionCreate(txn)
  local r = kasas.fetch{
    url = "https://api.example.com/x",
    method = "POST",
    headers = { ["X-Test"] = "1" },
    body = "hi",
    timeout_ms = 500,
  }
  kasas.apply_labels(txn.id, { status = tostring(r.status) })
end
